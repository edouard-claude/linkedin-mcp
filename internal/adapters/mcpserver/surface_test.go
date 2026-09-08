package mcpserver

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// allTools is the complete surface the server is expected to expose.
var allTools = []string{
	// lecture
	"connection_status", "list_posts", "post_engagement", "post_comments",
	"post_analytics", "account_analytics", "reconnect_url",
	// écriture
	"publish_post", "edit_post", "publish_comment", "react",
	// destruction
	"delete_post", "delete_comment",
}

func TestEveryToolIsRegistered(t *testing.T) {
	h := newServerHarness(t)
	res, err := h.connect(t, "token-a").ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	got := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	for _, name := range allTools {
		tool, ok := got[name]
		if !ok {
			t.Errorf("outil manquant: %s", name)
			continue
		}
		if tool.Description == "" {
			t.Errorf("%s: description vide", name)
		}
	}
	if len(got) != len(allTools) {
		t.Errorf("%d outils exposés, %d attendus", len(got), len(allTools))
	}

	// The tools that destroy content must say so through their annotations,
	// which is what lets a client warn before running them.
	for _, name := range []string{"delete_post", "delete_comment"} {
		tool := got[name]
		if tool == nil || tool.Annotations == nil || tool.Annotations.DestructiveHint == nil ||
			!*tool.Annotations.DestructiveHint {
			t.Errorf("%s n'est pas annoté comme destructif", name)
		}
	}
	// Every write tool must spell out the confirmation rule, because that is
	// the only thing standing between a model and a public post.
	for _, name := range []string{"publish_post", "edit_post", "publish_comment",
		"react", "delete_post", "delete_comment"} {
		if tool := got[name]; tool != nil && !strings.Contains(tool.Description, "confirm=true") {
			t.Errorf("%s ne documente pas la confirmation", name)
		}
	}
}

func TestWriteToolsNeedConfirmation(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"publish_post", map[string]any{"commentary": "Bonjour LinkedIn"}},
		{"edit_post", map[string]any{"post_urn": "urn:li:share:aaa", "commentary": "corrigé"}},
		{"publish_comment", map[string]any{"object_urn": "urn:li:share:aaa", "message": "merci"}},
		{"react", map[string]any{"object_urn": "urn:li:share:aaa"}},
		{"delete_post", map[string]any{"post_urn": "urn:li:share:aaa"}},
		{"delete_comment", map[string]any{"object_urn": "urn:li:share:aaa", "comment_id": "c1"}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			h := newServerHarness(t)
			payload, isErr := call(t, h.connect(t, "token-a"), tc.tool, tc.args)
			if isErr {
				t.Fatalf("erreur: %s", payload)
			}
			if !strings.Contains(payload, `"preview":true`) {
				t.Fatalf("ce n'est pas un aperçu: %s", payload)
			}
			for _, name := range h.api.recorded() {
				if name != "GetPost" && name != "SocialCounts" && name != "Comments" {
					t.Fatalf("appel d'écriture sans confirmation: %v", h.api.recorded())
				}
			}
		})
	}
}

// TestWritesRespectTenantIsolation is the guarantee the whole design exists
// for: knowing another member's post URN must not be enough to touch it.
func TestWritesRespectTenantIsolation(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"edit_post", map[string]any{"post_urn": "urn:li:share:bbb", "commentary": "pirate", "confirm": true}},
		{"delete_post", map[string]any{"post_urn": "urn:li:share:bbb", "confirm": true}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			h := newServerHarness(t)
			payload, isErr := call(t, h.connect(t, "token-a"), tc.tool, tc.args)
			if !isErr {
				t.Fatalf("action acceptée sur la publication d'un autre membre: %s", payload)
			}
			if len(h.api.recorded()) != 0 {
				t.Fatalf("appel LinkedIn vers un autre membre: %v", h.api.recorded())
			}
		})
	}
}

func TestPublishConfirmedReachesLinkedInOnce(t *testing.T) {
	h := newServerHarness(t)
	payload, isErr := call(t, h.connect(t, "token-a"), "publish_post", map[string]any{
		"commentary": "Bonjour LinkedIn", "confirm": true,
	})
	if isErr {
		t.Fatalf("erreur: %s", payload)
	}
	out := decodeJSON[map[string]any](t, payload)
	if out["post_urn"] != "urn:li:share:1000" {
		t.Fatalf("sortie = %s", payload)
	}
	if got := h.api.recorded(); len(got) != 1 || got[0] != "CreatePost" {
		t.Fatalf("appels = %v", got)
	}

	// The post is now in the ledger, which is what makes it listable.
	posts, err := h.store.LedgerPosts(t.Context(), "tenant-a", false, 10)
	if err != nil {
		t.Fatalf("LedgerPosts: %v", err)
	}
	if len(posts) != 2 {
		t.Fatalf("registre = %+v", posts)
	}
}

func TestResourcesAreTenantScoped(t *testing.T) {
	h := newServerHarness(t)

	res, err := h.connect(t, "token-a").ListResources(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(res.Resources) != 2 {
		t.Fatalf("%d ressources", len(res.Resources))
	}

	read := func(token, uri string) string {
		t.Helper()
		out, err := h.connect(t, token).ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("ReadResource %s: %v", uri, err)
		}
		if len(out.Contents) != 1 {
			t.Fatalf("contenus = %+v", out.Contents)
		}
		return out.Contents[0].Text
	}

	a := read("token-a", uriPosts)
	if !strings.Contains(a, "urn:li:share:aaa") || strings.Contains(a, "urn:li:share:bbb") {
		t.Fatalf("fuite dans la ressource du tenant A: %s", a)
	}
	b := read("token-b", uriPosts)
	if !strings.Contains(b, "urn:li:share:bbb") || strings.Contains(b, "urn:li:share:aaa") {
		t.Fatalf("fuite dans la ressource du tenant B: %s", b)
	}

	// No resource may ever carry a LinkedIn access token.
	for _, text := range []string{a, b, read("token-a", uriConnection)} {
		if strings.Contains(text, "AQV-") {
			t.Fatalf("jeton LinkedIn exposé: %s", text)
		}
	}
	if !strings.Contains(read("token-a", uriConnection), `"capabilities"`) {
		t.Fatal("la ressource de connexion n'annonce pas les capacités")
	}
}

func TestPromptsAreOffered(t *testing.T) {
	h := newServerHarness(t)
	session := h.connect(t, "token-a")

	list, err := session.ListPrompts(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	names := map[string]bool{}
	for _, p := range list.Prompts {
		names[p.Name] = true
	}
	for _, want := range []string{"bilan_publications", "redaction_publication", "reponses_commentaires"} {
		if !names[want] {
			t.Errorf("prompt manquant: %s", want)
		}
	}

	got, err := session.GetPrompt(t.Context(), &mcp.GetPromptParams{
		Name:      "redaction_publication",
		Arguments: map[string]string{"sujet": "le MCP LinkedIn"},
	})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("messages = %+v", got.Messages)
	}
	text, ok := got.Messages[0].Content.(*mcp.TextContent)
	if !ok {
		t.Fatalf("contenu de type %T", got.Messages[0].Content)
	}
	if !strings.Contains(text.Text, "le MCP LinkedIn") {
		t.Fatalf("le prompt n'a pas pris son argument: %s", text.Text)
	}
	// A drafting prompt must never publish on its own.
	if !strings.Contains(text.Text, "confirm") {
		t.Fatalf("le prompt n'impose pas la confirmation: %s", text.Text)
	}

	// An omitted argument falls back rather than leaving an empty hole.
	got, err = session.GetPrompt(t.Context(), &mcp.GetPromptParams{Name: "bilan_publications"})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	text = got.Messages[0].Content.(*mcp.TextContent)
	if !strings.Contains(text.Text, "list_posts") {
		t.Fatalf("prompt = %s", text.Text)
	}
}

func TestUnauthenticatedRequestAdvertisesTheAuthServer(t *testing.T) {
	h := newServerHarness(t)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	_, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: h.url, DisableStandaloneSSE: true,
	}, nil)
	if err == nil {
		t.Fatal("connexion sans jeton acceptée")
	}
}

// TestServerAdvertisesItsIdentity pins what a client needs to show this server
// as itself: a title, a website and an icon, all absolute so they resolve from
// anywhere.
func TestServerAdvertisesItsIdentity(t *testing.T) {
	h := newServerHarness(t)
	session := h.connect(t, "token-a")

	info := session.InitializeResult().ServerInfo
	if info.Title == "" || info.Version == "" {
		t.Fatalf("identité incomplète: %+v", info)
	}
	if info.WebsiteURL != "https://li.example.re" {
		t.Fatalf("site = %q", info.WebsiteURL)
	}
	if len(info.Icons) != 1 {
		t.Fatalf("icônes = %+v", info.Icons)
	}
	icon := info.Icons[0]
	if icon.Source != "https://li.example.re/icon.png" || icon.MIMEType != "image/png" {
		t.Fatalf("icône = %+v", icon)
	}
}
