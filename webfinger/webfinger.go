package webfinger

type Link struct {
	Rel string `json:"rel"`
	// subscribe テンプレートのように Type や Href を持たない Link が
	// あるので省略できるようにしてある。
	Type     string `json:"type,omitempty"`
	Href     string `json:"href,omitempty"`
	Template string `json:"template,omitempty"`
}

type WebFingerUserResource struct {
	Subject string   `json:"subject"`
	Aliases []string `json:"aliases"`
	Links   []Link   `json:"links"`
}

// NewWebFingerUserResource は JRD を組む。account には正規のハンドルを
// 渡す。別名で引かれた場合もここが正規のものを返すことで、リモートは
// 正しいハンドルへ誘導される。
func NewWebFingerUserResource(account string, profile string, origin string) WebFingerUserResource {
	return WebFingerUserResource{
		Subject: "acct:" + account,
		Aliases: []string{profile},
		Links: []Link{
			{
				Rel:  "http://webfinger.net/rel/profile-page",
				Type: "text/html",
				Href: profile,
			},
			{
				Rel:  "self",
				Type: "application/activity+json",
				Href: profile,
			},
			{
				// 他インスタンスのリモートフォローボタンから辿れるように
				// する。{uri} には辿ってきた側のハンドルが入る。
				Rel:      "http://ostatus.org/schema/1.0/subscribe",
				Template: origin + "/authorize_interaction?uri={uri}",
			},
		},
	}
}
