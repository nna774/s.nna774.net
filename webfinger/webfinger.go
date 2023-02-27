package webfinger

type Link struct {
	Rel  string `json:"rel"`
	Type string `json:"type"`
	Href string `json:"href"`
}

type WebFingerUserResource struct {
	Subject string   `json:"subject"`
	Aliases []string `json:"aliases"`
	Links   []Link   `json:"links"`
}

func NewWebFingerUserResource(account string, profile string) WebFingerUserResource {
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
		},
	}
}
