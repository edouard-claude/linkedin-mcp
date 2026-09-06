package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (d *deps) registerPrompts(srv *mcp.Server) {
	srv.AddPrompt(&mcp.Prompt{
		Name:        "bilan_publications",
		Title:       "Bilan des publications",
		Description: "Fait le point sur les publications récentes : ce qui a marché, ce qui n'a pas pris, et ce qu'il faut en retenir pour la suite.",
		Arguments: []*mcp.PromptArgument{
			{Name: "periode", Description: "Période à couvrir, en clair : « ce mois-ci », « les 3 derniers mois ».", Required: false},
		},
	}, d.promptReview)

	srv.AddPrompt(&mcp.Prompt{
		Name:        "redaction_publication",
		Title:       "Rédiger une publication",
		Description: "Écrit une publication dans la voix de l'utilisateur, en s'appuyant sur ce qui a déjà bien fonctionné sur son compte.",
		Arguments: []*mcp.PromptArgument{
			{Name: "sujet", Description: "Le sujet de la publication.", Required: true},
		},
	}, d.promptWrite)

	srv.AddPrompt(&mcp.Prompt{
		Name:        "reponses_commentaires",
		Title:       "Répondre aux commentaires",
		Description: "Passe en revue les commentaires reçus et propose des réponses, sans rien publier sans accord.",
		Arguments: []*mcp.PromptArgument{
			{Name: "post_urn", Description: "Publication à traiter. Absent : les plus récentes.", Required: false},
		},
	}, d.promptReplies)
}

func (d *deps) promptReview(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	periode := argOr(req, "periode", "les publications récentes")

	body := fmt.Sprintf(`Tu fais le bilan de %s sur le compte LinkedIn de l'utilisateur.

Marche à suivre :
1. connection_status, pour savoir ce qui est accessible. Si les statistiques
   ne le sont pas, dis-le une fois et continue avec l'engagement.
2. list_posts. Regarde le champ source : s'il vaut "ledger", préviens que la
   liste ne couvre que les publications faites depuis ce serveur.
3. Pour chaque publication, post_engagement. Si les statistiques sont
   accessibles, post_analytics apporte les impressions, les comptes atteints,
   les abonnés gagnés et les vues de profil, qui disent bien plus que les
   likes.
4. Rédige le bilan en français : ce qui a le mieux marché et l'hypothèse la
   plus plausible, ce qui n'a pas pris, et deux ou trois choses concrètes à
   essayer ensuite.

Ne publie rien : ce bilan est en lecture seule, n'appelle aucun outil
d'écriture. Ne compare pas des publications d'âges très différents sans le
signaler : une publication d'hier n'a pas fini d'accumuler ses vues.`, periode)

	return promptResult("Bilan des publications", body), nil
}

func (d *deps) promptWrite(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	sujet := argOr(req, "sujet", "le sujet que l'utilisateur va te donner")

	body := fmt.Sprintf(`Tu écris une publication LinkedIn sur : %s.

Avant d'écrire, lis ce qui existe. Appelle list_posts et post_engagement sur
les publications qui ont le mieux marché, pour repérer la voix de
l'utilisateur : longueur, ton, usage des retours à la ligne, présence ou non
d'émojis et de hashtags. Imite cette voix, pas une idée générique du bon post
LinkedIn.

Propose ensuite le texte à l'utilisateur, tel qu'il sera publié. Attends son
accord ou ses corrections. Ce n'est qu'ensuite que tu appelles publish_post
avec confirm=true.

N'invente aucun fait, aucun chiffre et aucune anecdote personnelle : si le
texte a besoin de quelque chose que tu ignores, demande-le.`, sujet)

	return promptResult("Rédiger une publication", body), nil
}

func (d *deps) promptReplies(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	cible := argOr(req, "post_urn", "les publications les plus récentes")

	body := fmt.Sprintf(`Tu traites les commentaires reçus sur %s.

Marche à suivre :
1. list_posts, puis post_engagement pour repérer celles qui ont des
   commentaires.
2. post_comments sur celles-là.
3. Classe : ce qui appelle une vraie réponse, ce à quoi une réaction suffit,
   et ce qui ne mérite rien.
4. Pour chaque réponse, propose le texte et attends l'accord de l'utilisateur.

Règles :
- N'écris rien sans confirmation. publish_comment, react et delete_comment
  renvoient un aperçu tant que confirm=true est absent.
- Réponds dans la langue du commentaire.
- Ne propose jamais delete_comment de toi-même sur le commentaire de
  quelqu'un d'autre : supprimer est définitif et se remarque.`, cible)

	return promptResult("Répondre aux commentaires", body), nil
}

// argOr reads a prompt argument, falling back when the client omitted it.
func argOr(req *mcp.GetPromptRequest, name, fallback string) string {
	if req == nil || req.Params == nil {
		return fallback
	}
	if v := req.Params.Arguments[name]; v != "" {
		return v
	}
	return fallback
}

func promptResult(description, body string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{
		Description: description,
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: body},
		}},
	}
}
