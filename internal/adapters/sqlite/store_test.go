package sqlite

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/adapters/crypto"
	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	cipher, err := crypto.New(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	store, err := New(t.Context(), filepath.Join(t.TempDir(), "test.db"), cipher)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func seedTenant(t *testing.T, s *Store, id, memberID, token string) *domain.Tenant {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	tenant := &domain.Tenant{
		ID:             id,
		MemberID:       memberID,
		DisplayName:    "Membre " + id,
		AccessToken:    token,
		TokenExpiresAt: now.Add(60 * 24 * time.Hour),
		Scopes:         []string{"openid", "w_member_social"},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.UpsertTenant(t.Context(), tenant); err != nil {
		t.Fatalf("UpsertTenant: %v", err)
	}
	return tenant
}

func TestTenantRoundTripAndTokensAreEncryptedAtRest(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	const token = "AQV-linkedin-access-token"
	seeded := seedTenant(t, s, "tenant-a", "member-1", token)
	seeded.RefreshToken = "AQW-refresh"
	if err := s.UpsertTenant(ctx, seeded); err != nil {
		t.Fatalf("UpsertTenant: %v", err)
	}

	got, err := s.TenantByMemberID(ctx, "member-1")
	if err != nil {
		t.Fatalf("TenantByMemberID: %v", err)
	}
	if got.AccessToken != token || got.RefreshToken != "AQW-refresh" {
		t.Fatalf("jetons relus = %q / %q", got.AccessToken, got.RefreshToken)
	}
	if got.ID != "tenant-a" || len(got.Scopes) != 2 || got.Scopes[1] != "w_member_social" {
		t.Fatalf("tenant = %+v", got)
	}
	if !got.TokenExpiresAt.Equal(seeded.TokenExpiresAt) {
		t.Fatalf("échéance = %v, attendue %v", got.TokenExpiresAt, seeded.TokenExpiresAt)
	}

	// The disk must never hold a usable LinkedIn token: a stolen database
	// file is worth nothing without the cipher key.
	var access, refresh []byte
	if err := s.db.QueryRowContext(ctx,
		`SELECT access_token_enc, refresh_token_enc FROM tenants WHERE id = ?`, "tenant-a").
		Scan(&access, &refresh); err != nil {
		t.Fatalf("lecture brute: %v", err)
	}
	if bytes.Contains(access, []byte(token)) || bytes.Contains(refresh, []byte("AQW-refresh")) {
		t.Fatal("un jeton est stocké en clair")
	}
}

func TestUpsertTenantKeepsCreatedAt(t *testing.T) {
	s := newTestStore(t)
	first := seedTenant(t, s, "tenant-a", "member-1", "T1")

	later := first.CreatedAt.Add(48 * time.Hour)
	if err := s.UpsertTenant(t.Context(), &domain.Tenant{
		ID: "tenant-a", MemberID: "member-1", DisplayName: "Renommé",
		AccessToken: "T2", CreatedAt: later, UpdatedAt: later,
	}); err != nil {
		t.Fatalf("UpsertTenant: %v", err)
	}

	got, err := s.TenantByID(t.Context(), "tenant-a")
	if err != nil {
		t.Fatalf("TenantByID: %v", err)
	}
	if !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("created_at écrasé: %v", got.CreatedAt)
	}
	if got.AccessToken != "T2" || got.DisplayName != "Renommé" {
		t.Fatalf("tenant = %+v", got)
	}
}

func TestUnknownTenantIsNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.TenantByID(t.Context(), "inconnu"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("erreur = %v", err)
	}
	if _, err := s.TenantByMemberID(t.Context(), "inconnu"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("erreur = %v", err)
	}
}

// TestTenantsDueForTokenRefresh covers both reasons to look at a token again:
// it is about to expire, or it simply has not been checked in a long time.
func TestTenantsDueForTokenRefresh(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	upsert := func(id string, expires, updated time.Time) {
		t.Helper()
		if err := s.UpsertTenant(ctx, &domain.Tenant{
			ID: id, MemberID: "m-" + id, DisplayName: id, AccessToken: "T",
			TokenExpiresAt: expires, CreatedAt: now, UpdatedAt: updated,
		}); err != nil {
			t.Fatalf("UpsertTenant %s: %v", id, err)
		}
	}
	upsert("bientot", now.Add(2*24*time.Hour), now) // expire sous peu
	upsert("loin", now.Add(50*24*time.Hour), now)   // rien à faire
	// Sans échéance connue, c'est la date du dernier examen qui décide.
	upsert("oublie", time.Time{}, now.Add(-30*24*time.Hour))
	upsert("revu", time.Time{}, now)

	due, err := s.TenantsDueForTokenRefresh(ctx,
		now.Add(7*24*time.Hour), now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("TenantsDueForTokenRefresh: %v", err)
	}
	ids := map[string]bool{}
	for _, tenant := range due {
		ids[tenant.ID] = true
		if tenant.AccessToken != "T" {
			t.Fatalf("le jeton n'a pas été déchiffré: %+v", tenant)
		}
	}
	if !ids["bientot"] || !ids["oublie"] || ids["loin"] || ids["revu"] {
		t.Fatalf("sélection = %v", ids)
	}
}

// TestLedgerIsTheMemoryOfWhatWePublished covers the whole reason the ledger
// exists: LinkedIn withholds r_member_social, so the server has to remember.
func TestLedgerIsTheMemoryOfWhatWePublished(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedTenant(t, s, "tenant-a", "member-1", "T")
	seedTenant(t, s, "tenant-b", "member-2", "T")
	now := time.Now().UTC().Truncate(time.Second)

	for i, urn := range []string{"urn:li:share:1", "urn:li:share:2"} {
		if err := s.RecordPost(ctx, "tenant-a", domain.LedgerPost{
			PostURN: urn, Commentary: "texte " + urn, Visibility: "PUBLIC",
			Permalink: "https://www.linkedin.com/feed/update/" + urn,
			CreatedAt: now.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("RecordPost: %v", err)
		}
	}
	if err := s.RecordPost(ctx, "tenant-b", domain.LedgerPost{
		PostURN: "urn:li:share:9", CreatedAt: now,
	}); err != nil {
		t.Fatalf("RecordPost: %v", err)
	}

	posts, err := s.LedgerPosts(ctx, "tenant-a", false, 10)
	if err != nil {
		t.Fatalf("LedgerPosts: %v", err)
	}
	if len(posts) != 2 {
		t.Fatalf("%d publications: %+v", len(posts), posts)
	}
	// Most recent first: that is the order a feed is read in.
	if posts[0].PostURN != "urn:li:share:2" {
		t.Fatalf("ordre = %+v", posts)
	}
	if posts[0].Permalink == "" || posts[0].Visibility != "PUBLIC" {
		t.Fatalf("champs perdus: %+v", posts[0])
	}

	// A post belongs to one member, and is invisible to every other.
	if _, err := s.LedgerPost(ctx, "tenant-a", "urn:li:share:9"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("fuite entre tenants: %v", err)
	}

	if err := s.UpdateLedgerCommentary(ctx, "tenant-a", "urn:li:share:1", "corrigé", now); err != nil {
		t.Fatalf("UpdateLedgerCommentary: %v", err)
	}
	one, err := s.LedgerPost(ctx, "tenant-a", "urn:li:share:1")
	if err != nil {
		t.Fatalf("LedgerPost: %v", err)
	}
	if one.Commentary != "corrigé" || one.UpdatedAt.IsZero() {
		t.Fatalf("publication = %+v", one)
	}

	// Deletion is a tombstone, not a row removal: the history stays readable.
	if err := s.MarkPostDeleted(ctx, "tenant-a", "urn:li:share:1", now); err != nil {
		t.Fatalf("MarkPostDeleted: %v", err)
	}
	live, _ := s.LedgerPosts(ctx, "tenant-a", false, 10)
	if len(live) != 1 || live[0].PostURN != "urn:li:share:2" {
		t.Fatalf("publications vivantes = %+v", live)
	}
	all, _ := s.LedgerPosts(ctx, "tenant-a", true, 10)
	if len(all) != 2 {
		t.Fatalf("historique = %+v", all)
	}
	gone, _ := s.LedgerPost(ctx, "tenant-a", "urn:li:share:1")
	if !gone.Deleted() {
		t.Fatalf("la suppression n'est pas mémorisée: %+v", gone)
	}
}

func TestRecordPostIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedTenant(t, s, "tenant-a", "member-1", "T")
	now := time.Now().UTC().Truncate(time.Second)

	post := domain.LedgerPost{PostURN: "urn:li:share:1", Commentary: "v1", CreatedAt: now}
	if err := s.RecordPost(ctx, "tenant-a", post); err != nil {
		t.Fatalf("RecordPost: %v", err)
	}
	post.Commentary = "v2"
	if err := s.RecordPost(ctx, "tenant-a", post); err != nil {
		t.Fatalf("RecordPost (rejeu): %v", err)
	}
	posts, _ := s.LedgerPosts(ctx, "tenant-a", true, 10)
	if len(posts) != 1 || posts[0].Commentary != "v2" {
		t.Fatalf("publications = %+v", posts)
	}
}

func TestDeleteTenantTakesItsLedgerAlong(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedTenant(t, s, "tenant-a", "member-1", "T")
	if err := s.RecordPost(ctx, "tenant-a", domain.LedgerPost{
		PostURN: "urn:li:share:1", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordPost: %v", err)
	}

	if err := s.DeleteTenant(ctx, "tenant-a"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if _, err := s.TenantByID(ctx, "tenant-a"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("le tenant existe encore: %v", err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM posts WHERE tenant_id = ?`, "tenant-a").Scan(&count); err != nil {
		t.Fatalf("comptage: %v", err)
	}
	if count != 0 {
		t.Fatalf("%d publications orphelines", count)
	}
}

func TestAuthCodeIsSingleUse(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedTenant(t, s, "tenant-a", "member-1", "T")
	now := time.Now().UTC().Truncate(time.Second)
	code := &domain.AuthCode{
		Code: "the-code", ClientID: "cid", TenantID: "tenant-a",
		RedirectURI: "https://claude.ai/cb", CodeChallenge: "abc",
		Resource: "https://li.example.re/mcp", ExpiresAt: now.Add(time.Minute),
	}
	if err := s.CreateAuthCode(ctx, code); err != nil {
		t.Fatalf("CreateAuthCode: %v", err)
	}

	got, err := s.ConsumeAuthCode(ctx, "the-code")
	if err != nil {
		t.Fatalf("ConsumeAuthCode: %v", err)
	}
	if got.TenantID != "tenant-a" || got.CodeChallenge != "abc" || got.Resource != code.Resource {
		t.Fatalf("code = %+v", got)
	}
	if _, err := s.ConsumeAuthCode(ctx, "the-code"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rejeu accepté: %v", err)
	}
}

func TestRefreshTokenRotation(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedTenant(t, s, "tenant-a", "member-1", "T")
	now := time.Now().UTC().Truncate(time.Second)
	rt := &domain.RefreshToken{
		TokenHash: "hash-1", ClientID: "cid", TenantID: "tenant-a",
		ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := s.CreateRefreshToken(ctx, rt); err != nil {
		t.Fatalf("CreateRefreshToken: %v", err)
	}

	got, err := s.RotateRefreshToken(ctx, "hash-1", now)
	if err != nil {
		t.Fatalf("RotateRefreshToken: %v", err)
	}
	if got.TenantID != "tenant-a" {
		t.Fatalf("jeton = %+v", got)
	}
	// A rotated token is dead: replaying it must not mint a new session.
	if _, err := s.RotateRefreshToken(ctx, "hash-1", now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rejeu accepté: %v", err)
	}

	if err := s.CreateRefreshToken(ctx, &domain.RefreshToken{
		TokenHash: "hash-2", ClientID: "cid", TenantID: "tenant-a",
		ExpiresAt: now.Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("CreateRefreshToken: %v", err)
	}
	if err := s.RevokeTenantRefreshTokens(ctx, "tenant-a"); err != nil {
		t.Fatalf("RevokeTenantRefreshTokens: %v", err)
	}
	if _, err := s.RotateRefreshToken(ctx, "hash-2", now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("un jeton révoqué reste utilisable: %v", err)
	}
}

func TestPurgeExpiredKeepsWhatIsStillValid(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedTenant(t, s, "tenant-a", "member-1", "T")
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.CreateAuthCode(ctx, &domain.AuthCode{
		Code: "vieux", ClientID: "cid", TenantID: "tenant-a", ExpiresAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("CreateAuthCode: %v", err)
	}
	if err := s.CreateLoginState(ctx, &domain.LoginState{
		State: "frais", ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateLoginState: %v", err)
	}
	if err := s.CreateLoginState(ctx, &domain.LoginState{
		State: "perime", ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("CreateLoginState: %v", err)
	}

	if err := s.PurgeExpired(ctx, now); err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if _, err := s.ConsumeAuthCode(ctx, "vieux"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("code expiré conservé: %v", err)
	}
	if _, err := s.ConsumeLoginState(ctx, "perime"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("state expiré conservé: %v", err)
	}
	if _, err := s.ConsumeLoginState(ctx, "frais"); err != nil {
		t.Fatalf("un state valide a été purgé: %v", err)
	}
}
