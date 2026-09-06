package linkedin

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/edouard-claude/linkedin-mcp/internal/app"
	"github.com/edouard-claude/linkedin-mcp/internal/domain"
	"github.com/edouard-claude/linkedin-mcp/web"
)

// CodeIssuer mints the MCP authorization code once LinkedIn has told us who
// the member is. It is declared here, at the point of use, so this adapter
// never imports the authorization server.
type CodeIssuer interface {
	IssueAuthCode(r *http.Request, req domain.OAuthRequest, tenantID string) (string, error)
}

// HandlerOptions configures the LinkedIn facing HTTP surface.
type HandlerOptions struct {
	PublicURL   string
	RedirectURI string
}

// Handlers serves the pages and callbacks of the LinkedIn login flow.
type Handlers struct {
	login  *app.LoginService
	issuer CodeIssuer
	opts   HandlerOptions
	logger *slog.Logger
}

// NewHandlers wires the LinkedIn HTTP surface.
func NewHandlers(login *app.LoginService, issuer CodeIssuer, opts HandlerOptions, logger *slog.Logger) *Handlers {
	return &Handlers{login: login, issuer: issuer, opts: opts, logger: logger}
}

// LoginHandler serves GET /linkedin/login?state=… by sending the browser
// straight to the LinkedIn consent page.
func (h *Handlers) LoginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")
		if state == "" {
			h.renderError(w, http.StatusBadRequest, "Lien incomplet",
				"Cette adresse doit être ouverte depuis votre client MCP.", "")
			return
		}
		http.Redirect(w, r, h.login.AuthorizeURL(h.opts.RedirectURI, state), http.StatusFound)
	})
}

// CallbackHandler serves GET /linkedin/callback, the redirect target
// registered in the LinkedIn app.
func (h *Handlers) CallbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		state := q.Get("state")
		if state == "" {
			h.renderError(w, http.StatusBadRequest, "Connexion impossible",
				"Le paramètre state est absent.", "")
			return
		}

		// The state is single use: consuming it here closes the CSRF window
		// whatever happens next.
		login, err := h.login.ConsumeState(r.Context(), state)
		if errors.Is(err, domain.ErrNotFound) {
			h.renderError(w, http.StatusBadRequest, "Session expirée",
				"La demande de connexion a expiré ou a déjà été utilisée.",
				"Relancez la connexion depuis votre client MCP.")
			return
		}
		if err != nil {
			h.logger.Error("lecture du state de login", "error", err)
			h.renderError(w, http.StatusInternalServerError, "Erreur interne",
				"Impossible de retrouver votre demande de connexion.", "")
			return
		}

		// A reconnection link carries no MCP client: the flow then ends on a
		// confirmation page instead of a redirect.
		isReconnect := login.Request.ClientID == ""

		if liErr := q.Get("error"); liErr != "" {
			h.logger.Info("autorisation LinkedIn refusée", "reason", liErr)
			if isReconnect {
				h.renderError(w, http.StatusOK, "Autorisation refusée",
					"Vous avez refusé l'accès à LinkedIn, rien n'a été modifié.", "")
				return
			}
			h.redirectToClient(w, r, login.Request, map[string]string{
				"error":             "access_denied",
				"error_description": "autorisation LinkedIn refusée",
			})
			return
		}

		code := q.Get("code")
		if code == "" {
			h.renderError(w, http.StatusBadRequest, "Connexion impossible",
				"LinkedIn n'a renvoyé aucun code d'autorisation.", "")
			return
		}

		result, err := h.login.Complete(r.Context(), code, h.opts.RedirectURI)
		if errors.Is(err, domain.ErrForbiddenMember) {
			h.renderError(w, http.StatusForbidden, "Compte non autorisé",
				domain.ErrForbiddenMember.Error(),
				"Demandez à l'administrateur d'ajouter votre compte.")
			return
		}
		if err != nil {
			h.logger.Error("finalisation du login LinkedIn", "error", err)
			h.renderError(w, http.StatusBadGateway, "Connexion impossible",
				"LinkedIn a refusé la demande de connexion.", userDetail(err))
			return
		}
		h.logger.Info("membre connecté", "tenant_id", result.TenantID, "scopes", len(result.Scopes))

		if isReconnect {
			h.render(w, http.StatusOK, web.PageReconnected, web.ReconnectedData{
				Title:       "Compte reconnecté",
				DisplayName: result.DisplayName,
			})
			return
		}

		target, err := h.issuer.IssueAuthCode(r, login.Request, result.TenantID)
		if err != nil {
			h.logger.Error("émission du code d'autorisation", "error", err)
			h.renderError(w, http.StatusInternalServerError, "Erreur interne",
				"Impossible de finaliser l'autorisation.", "")
			return
		}
		http.Redirect(w, r, target, http.StatusFound)
	})
}

// PrivacyHandler serves the static privacy policy the LinkedIn app needs.
func (h *Handlers) PrivacyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.render(w, http.StatusOK, web.PagePrivacy, web.PrivacyData{Title: "Politique de confidentialité"})
	})
}

// redirectToClient sends the MCP client back to its redirect_uri.
func (h *Handlers) redirectToClient(w http.ResponseWriter, r *http.Request, req domain.OAuthRequest, params map[string]string) {
	u, err := url.Parse(req.RedirectURI)
	if err != nil {
		h.renderError(w, http.StatusBadRequest, "Connexion impossible",
			"L'adresse de retour du client MCP est invalide.", "")
		return
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	if req.ClientState != "" {
		q.Set("state", req.ClientState)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (h *Handlers) render(w http.ResponseWriter, status int, page string, data any) {
	if err := web.Render(w, status, page, data); err != nil {
		h.logger.Error("rendu HTML", "page", page, "error", err)
	}
}

func (h *Handlers) renderError(w http.ResponseWriter, status int, title, message, detail string) {
	h.render(w, status, web.PageError, web.ErrorData{Title: title, Message: message, Detail: detail})
}

// userDetail turns an API failure into a sentence the member can act on.
func userDetail(err error) string {
	if ae, ok := domain.AsAPIError(err); ok {
		return ae.UserMessage()
	}
	return ""
}
