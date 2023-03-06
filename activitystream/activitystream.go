package activitystream

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

const ActivityStreamsContentType = `application/ld+json; profile="https://www.w3.org/ns/activitystreams"`

const (
	CreateType                = "Create"
	NoteType                  = "Note"
	OrderedCollectionPageType = "OrderedCollectionPage"
)

type Object struct {
	baseObject
	value interface{}
}

func (o *Object) UnmarshalJSON(data []byte) error {
	var obj baseObject
	err := json.Unmarshal(data, &obj)
	if err != nil {
		return err
	}
	o.baseObject = obj
	log.Printf("unmarshal with type %v", obj.Type)
	switch obj.Type {
	case CreateType:
		var val Activity
		err := json.Unmarshal(data, &val)
		if err != nil {
			return err
		}
		o.value = val
	}
	return nil
}

func (o *Object) MarshalJSON() ([]byte, error) {
	log.Printf("called with type %v", o.Type)
	switch o.Type {
	case CreateType:
		act, _ := o.Activity()
		log.Printf("act is %+v", act)
		return json.Marshal(struct {
			baseObject
			Activity
		}{baseObject: o.baseObject, Activity: act})
	case OrderedCollectionPageType:
		ocp, _ := o.OrderedCollectionPage()
		return json.Marshal(struct {
			baseObject
			OrderedCollectionPage
		}{baseObject: o.baseObject, OrderedCollectionPage: ocp})
	default:
		log.Printf("marshal type: %v", o.Type)
		return json.Marshal(o.baseObject)
	}
}

func (o *Object) Activity() (Activity, bool) {
	switch o.Type {
	case CreateType:
		act, ok := o.value.(Activity)
		return act, ok
	default:
		return Activity{}, false
	}
}

func (o *Object) OrderedCollectionPage() (OrderedCollectionPage, bool) {
	if o.Type != OrderedCollectionPageType {
		return OrderedCollectionPage{}, false
	}
	ret, ok := o.value.(OrderedCollectionPage)
	return ret, ok
}

type baseObject struct {
	Context      interface{} `json:"@context,omitempty"`
	ID           string      `json:"id,omitempty"`
	Type         string      `json:"type,omitempty"`
	URL          string      `json:"url,omitempty"`
	Name         string      `json:"name,omitempty"`
	Icon         *Icon       `json:"icon,omitempty"`
	To           []string    `json:"to,omitempty"`
	Cc           []string    `json:"cc,omitempty"`
	Content      string      `json:"content,omitempty"`
	Published    string      `json:"published,omitempty"`
	AttributedTo string      `json:"attributedTo,omitempty"`
	Tag          []Object    `json:"tag,omitempty"`  // TODO: this may be object, not array
	Href         string      `json:"href,omitempty"` // activitypub-stream don't have this, but mastodon's tag has this
}

type Icon struct {
	Type      string `json:"type,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	URL       string `json:"url,omitempty"`
}

type Activity struct {
	Actor string `json:"actor,omitempty"` // TODO: 実はmaybe object
	Item  Object `json:"object,omitempty"`
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

func NewUserResource(ID string, name string, IconURI string, iconMediaType string, PreferredUsername string, inbox string, outbox string, followers string, following string, comment string, keyID string, publicKey string) *Object {
	return &Object{
		baseObject: baseObject{
			Context: []string{
				"https://www.w3.org/ns/activitystreams",
				"https://w3id.org/security/v1",
			},
			ID:   ID,
			Type: "Person",
			URL:  ID,
			Name: name,
			Icon: &Icon{
				Type:      "Image",
				MediaType: iconMediaType,
				URL:       IconURI,
			},
		},
		value: UserResource{
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
		},
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

func NewAccept(act Activity, actorID string, acceptID string) *Object {
	return &Object{
		baseObject: baseObject{
			Context: "https://www.w3.org/ns/activitystreams",
			ID:      acceptID,
			Type:    "Accept",
		},
		value: Accept{
			Activity: Activity{
				Actor: actorID,
			},
			Item: act,
		},
	}
}

func NewNote(noteID string, published string, name string, content string, attributedTo string, to []string, cc []string, tag []Object) *Object {
	return &Object{
		baseObject: baseObject{
			Context:      "https://www.w3.org/ns/activitystreams",
			ID:           noteID,
			Type:         CreateType,
			URL:          noteID,
			Published:    published,
			Name:         name,
			Content:      content,
			AttributedTo: attributedTo,
			To:           to,
			Cc:           cc,
			Tag:          tag,
		}}
}

func NewCreate(createID string, actor string, to []string, cc []string, obj Object) *Object {
	return &Object{
		baseObject: baseObject{
			Context: "https://www.w3.org/ns/activitystreams",
			ID:      createID,
			Type:    CreateType,
			To:      to,
			Cc:      cc,
		},
		value: Activity{
			Actor: actor,
			Item:  obj,
		},
	}
}

func NewTag(tagType string, name string, href string) Object {
	return Object{baseObject: baseObject{
		Type: tagType,
		Name: name,
		Href: href,
	}}
}

func NewMention(toID string) Object {
	return NewTag("Mention", "@kugayama@pawoo.net", toID) // TODO:
}

type Collection struct {
	TotalItems *int   `json:"totalItems,omitempty"`
	First      string `json:"first,omitempty"` // TODO: maybe Link
	Last       string `json:"last,omitempty"`
}

func NewOrderedCollection(id string, totalItems int, first string, last string) *Object {
	return &Object{
		baseObject: baseObject{
			Context: "https://www.w3.org/ns/activitystreams",
			ID:      id,
			Type:    "OrderedCollection",
		},
		value: Collection{
			TotalItems: &totalItems,
			First:      first,
			Last:       last,
		}}
}

type OrderedCollectionPage struct {
	Collection
	PartOf       string      `json:"partOf"`
	Next         string      `json:"next"`
	Prev         string      `json:"prev"`
	OrderedItems interface{} `json:"orderedItems"`
}

func NewOrderedCollectionPage(id string, partOf string, next string, prev string, orderdItems interface{}) *Object {
	return &Object{
		baseObject: baseObject{
			Context: "https://www.w3.org/ns/activitystreams",
			ID:      id,
			Type:    OrderedCollectionPageType,
		},
		value: OrderedCollectionPage{
			PartOf:       partOf,
			Next:         next,
			Prev:         prev,
			OrderedItems: orderdItems,
		},
	}
}
