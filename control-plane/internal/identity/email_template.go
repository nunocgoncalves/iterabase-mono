package identity

import (
	"fmt"
	"html"
	"strings"
)

// Public browser-journey paths carried by authentication email links. These are
// the SPA routes the frozen design uses.
const (
	AuthPathVerify = "/auth/verify"
	AuthPathSetup  = "/auth/setup"
	AuthPathReset  = "/auth/reset"
)

// AuthLinkURL builds the absolute one-time link for a purpose.
func AuthLinkURL(origin, purpose, token string) (string, error) {
	var path string
	switch purpose {
	case AuthLinkVerifyAccess:
		path = AuthPathVerify
	case AuthLinkSetupPassword:
		path = AuthPathSetup
	case AuthLinkResetPassword:
		path = AuthPathReset
	default:
		return "", fmt.Errorf("unknown auth link purpose %q", purpose)
	}
	return strings.TrimRight(origin, "/") + path + "?token=" + token, nil
}

// RenderAuthEmail renders the EN/PT subject, plain-text body, and HTML body in
// memory. Raw tokens are never persisted.
func RenderAuthEmail(purpose, locale, link, email string) (subject, text, htmlBody string, err error) {
	l := strings.ToLower(locale)
	if l != "pt" {
		l = "en"
	}
	displayEmail := html.EscapeString(email)
	switch purpose {
	case AuthLinkVerifyAccess:
		if l == "pt" {
			subject = "Confirme o seu endereço de email — Iterabase"
			text = fmt.Sprintf(`Pediu acesso ao Iterabase.

Confirme este endereço de email para enviar o seu pedido a um Administrador. A confirmação não concede acesso.

%s

Este link expira em 24 horas. Se não pediu acesso, pode ignorar esta mensagem.`, link)
			htmlBody = htmlEmail(displayEmail, "Confirme o seu endereço de email", "Confirme este endereço de email para enviar o seu pedido a um Administrador. A confirmação não concede acesso.", "Confirmar email", link, "Este link expira em 24 horas. Se não pediu acesso, pode ignorar esta mensagem.")
		} else {
			subject = "Verify your email address — Iterabase"
			text = fmt.Sprintf(`You asked for access to Iterabase.

Confirm this email address to send your request to an Admin for review. Verification does not grant access.

%s

This link expires in 24 hours. If you did not request access, you can ignore this message.`, link)
			htmlBody = htmlEmail(displayEmail, "Verify your email address", "Confirm this email address to send your request to an Admin for review. Verification does not grant access.", "Verify email", link, "This link expires in 24 hours. If you did not request access, you can ignore this message.")
		}
	case AuthLinkSetupPassword:
		if l == "pt" {
			subject = "Primeira configuração — Iterabase"
			text = fmt.Sprintf(`O seu pedido de acesso foi aprovado.

Conclua a configuração para escolher a sua palavra-passe. Concluir a configuração não inicia sessão.

%s

Este link expira em 7 dias.`, link)
			htmlBody = htmlEmail(displayEmail, "Primeira configuração", "O seu pedido de acesso foi aprovado. Conclua a configuração para escolher a sua palavra-passe. Concluir a configuração não inicia sessão.", "Concluir configuração", link, "Este link expira em 7 dias.")
		} else {
			subject = "First-time setup — Iterabase"
			text = fmt.Sprintf(`Your access request was approved.

Finish setup to choose your password. Finishing setup does not sign you in.

%s

This link expires in 7 days.`, link)
			htmlBody = htmlEmail(displayEmail, "First-time setup", "Your access request was approved. Finish setup to choose your password. Finishing setup does not sign you in.", "Finish setup", link, "This link expires in 7 days.")
		}
	case AuthLinkResetPassword:
		if l == "pt" {
			subject = "Repor a palavra-passe — Iterabase"
			text = fmt.Sprintf(`Recebemos um pedido para repor a palavra-passe.

Se foi você, escolha uma nova palavra-passe. Repor a palavra-passe termina todas as sessões do browser; as chaves de API não são revogadas.

%s

Este link expira em 30 minutos. Se não pediu isto, pode ignorar esta mensagem.`, link)
			htmlBody = htmlEmail(displayEmail, "Repor a palavra-passe", "Se foi você, escolha uma nova palavra-passe. Repor a palavra-passe termina todas as sessões do browser; as chaves de API não são revogadas.", "Repor palavra-passe", link, "Este link expira em 30 minutos. Se não pediu isto, pode ignorar esta mensagem.")
		} else {
			subject = "Reset your password — Iterabase"
			text = fmt.Sprintf(`We received a password-reset request.

If this was you, choose a new password. Resetting your password signs out all browser sessions; API keys are not revoked.

%s

This link expires in 30 minutes. If you did not request this, you can ignore this message.`, link)
			htmlBody = htmlEmail(displayEmail, "Reset your password", "If this was you, choose a new password. Resetting your password signs out all browser sessions; API keys are not revoked.", "Reset password", link, "This link expires in 30 minutes. If you did not request this, you can ignore this message.")
		}
	default:
		return "", "", "", fmt.Errorf("unknown auth email purpose %q", purpose)
	}
	return subject, text, htmlBody, nil
}

func htmlEmail(email, heading, body, action, link, footer string) string {
	escapedLink := html.EscapeString(link)
	return fmt.Sprintf(`<!doctype html>
<html><body style="font-family:system-ui,sans-serif;line-height:1.5">
<p>%s</p>
<h1 style="font-size:18px">%s</h1>
<p>%s</p>
<p><a href="%s">%s</a></p>
<p style="word-break:break-all"><a href="%s">%s</a></p>
<p style="color:#555">%s</p>
</body></html>`, email, html.EscapeString(heading), html.EscapeString(body), escapedLink, html.EscapeString(action), escapedLink, escapedLink, html.EscapeString(footer))
}
