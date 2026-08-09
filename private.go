package main

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/julienschmidt/httprouter"
	"github.com/nna774/s.nna774.net/auth"
	"github.com/nna774/s.nna774.net/httperror"
)

// authenticatorForRequest は要求されている私用エンドポイントに対応する
// Authenticator を返す。:user を持つ経路 (投稿の作成・削除など) はその
// Actor 専用のトークンで認証する。bot のトークンで nana のエンドポイントを
// 叩けたり、その逆が起きたりしないようにするため。:user を持たない経路
// (timeline・notifications・login 等) は primary actor 専用の機能なので
// primary の Authenticator を使う。
func authenticatorForRequest(r *http.Request) *auth.Authenticator {
	localPart := httprouter.ParamsFromContext(r.Context()).ByName("user")
	if localPart == "" {
		return primaryAuthenticator()
	}
	actor, ok := Config.ActorByLocalPart(localPart)
	if !ok {
		return nil
	}
	return authenticatorFor(actor)
}

// requireAuth は私用エンドポイントを認証で包む。
//
// mutating が true のとき、Cookie 経路では Sec-Fetch-Site が同一オリジンで
// あることを要求する (CSRF 対策)。Bearer 経路はブラウザが自動付与しない
// ため対象外。
//
// ActivityPub のエンドポイント (actor / inbox / outbox / .well-known) を
// これで包んではならない。連合が黙って壊れる。
func requireAuth(mutating bool, next httperror.HandleFuncWithError) httperror.HandleFuncWithError {
	return func(w http.ResponseWriter, r *http.Request) httperror.HttpError {
		a := authenticatorForRequest(r)
		if a == nil {
			return httperror.StatusNotFound("no such actor", nil)
		}
		err := a.Authorize(r, mutating)
		switch {
		case err == nil:
			return next(w, r)
		case errors.Is(err, auth.ErrCrossSite):
			return httperror.StatusForbidden("cross-site request rejected", nil)
		default:
			// ブラウザにはログイン画面を見せ、API 利用者には 401 を返す。
			if auth.WantsHTML(r) && r.Method == http.MethodGet {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return nil
			}
			return httperror.StatusUnauthorized("authentication required", nil)
		}
	}
}

// postLoginHandler はトークンを照合して署名付き Cookie を発行する。
func postLoginHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	token := ""
	if isFormRequest(r) {
		if err := r.ParseForm(); err != nil {
			return httperror.StatusBadRequest("bad form", err)
		}
		token = r.PostFormValue("token")
	} else {
		token = strings.TrimSpace(r.Header.Get("Authorization"))
		token = strings.TrimPrefix(token, "Bearer ")
	}
	if !primaryAuthenticator().CheckToken(token) {
		// ログイン失敗は理由を明かさない。
		return httperror.StatusUnauthorized("login failed", nil)
	}
	http.SetCookie(w, primaryAuthenticator().NewSessionCookie())

	if isFormRequest(r) {
		next := r.PostFormValue("next")
		if !isSafeRedirect(next) {
			next = "/timeline"
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
		return nil
	}
	respondText(w, http.StatusOK, "logged in\n")
	return nil
}

func postLogoutHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	http.SetCookie(w, primaryAuthenticator().ClearSessionCookie())
	if isFormRequest(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return nil
	}
	respondText(w, http.StatusOK, "logged out\n")
	return nil
}

// isSafeRedirect はログイン後の遷移先が自サイト内かを確かめる。外部への
// リダイレクトに使われるのを防ぐ。
func isSafeRedirect(next string) bool {
	if next == "" || !strings.HasPrefix(next, "/") {
		return false
	}
	// "//example.com" はプロトコル相対 URL で外部に飛ぶ。
	return !strings.HasPrefix(next, "//")
}

func hostOf(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	return u.Host
}
