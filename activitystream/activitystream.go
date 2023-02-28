package activitystream

import (
	"encoding/json"
	"fmt"
	"net/http"
)

const ActivityStreamsContentType = `application/ld+json; profile="https://www.w3.org/ns/activitystreams"`

type Object struct {
	Context      interface{} `json:"@context,omitempty"`
	ID           string      `json:"id,omitempty"`
	Type         string      `json:"type,omitempty"`
	URL          string      `json:"url,omitempty"`
	Name         string      `json:"name,omitempty"`
	Icon         Icon        `json:"icon,omitempty"`
	To           []string    `json:"to,omitempty"`
	Cc           []string    `json:"cc,omitempty"`
	Content      string      `json:"content,omitempty"`
	Published    string      `json:"published,omitempty"`
	AttributedTo string      `json:"attributedTo,omitempty"`
}

type Icon struct {
	Type      string `json:"type"`
	MediaType string `json:"mediaType"`
	URL       string `json:"url"`
}

type Activity struct {
	Object
	Actor string `json:"actor"`
}

func FetchActorInfo(actor string) (*UserResource, error) {
	req, err := http.NewRequest(http.MethodGet, actor, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("accept", ActivityStreamsContentType)
	resp, err := http.DefaultClient.Do(req) // TODO: maybe follow redurect
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetchActorInfo failed. actor %v not found", actor)
	}
	var ur UserResource
	err = json.NewDecoder(resp.Body).Decode(&ur)
	return &ur, err
}

type UserResource struct {
	Object            Object
	PreferredUsername string `json:"preferredUsername"`

	Inbox     string `json:"inbox"`
	Outbox    string `json:"outbox"`
	Summary   string `json:"summary"`
	Followers string `json:"followers"`
	Following string `json:"following"`
	Liked     string `json:"liked,omitempty"`

	PublicKey PublicKey `json:"publicKey"`
}

type PublicKey struct {
	ID           string `json:"id"`
	Owner        string `json:"owner"`
	PublicKeyPem string `json:"publicKeyPem"`
}

func NewUserResource(ID string, name string, IconURI string, iconMediaType string, PreferredUsername string, inbox string, outbox string, followers string, following string, comment string, keyID string, publicKey string) UserResource {
	return UserResource{
		Object: Object{
			Context: []string{
				"https://www.w3.org/ns/activitystreams",
				"https://w3id.org/security/v1",
			},
			ID:   ID,
			Type: "Person",
			URL:  ID,
			Name: name,
			Icon: Icon{
				Type:      "Image",
				MediaType: iconMediaType,
				URL:       IconURI,
			},
		},
		PreferredUsername: PreferredUsername,
		Inbox:             inbox,
		Outbox:            outbox,
		Followers:         followers,
		Following:         following,
		PublicKey: PublicKey{
			ID:           keyID,
			Owner:        ID,
			PublicKeyPem: publicKey,
		},

		Summary: comment,
	}
}

type ReceivedInbox struct {
	Activity

	Item json.RawMessage `json:"object"`
}
type Accept struct {
	Activity
	Item Activity `json:"object"`
}

func NewAccept(act Activity, actorID string, acceptID string) Accept {
	return Accept{
		Activity: Activity{
			Object: Object{
				Context: "https://www.w3.org/ns/activitystreams",
				ID:      acceptID,
				Type:    "Accept",
			},
			Actor: actorID,
		},
		Item: act,
	}
}

func NewNote(name, content string) Object {
	return Object{
		Context: "	https://www.w3.org/ns/activitystreams",
		Type:    "Note",
		Name:    name,
		Content: content,
	}
}
