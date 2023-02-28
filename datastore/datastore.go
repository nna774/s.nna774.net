package datastore

import (
	"encoding/json"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/guregu/dynamo"
)

const (
	partKey = "id"
	sortKey = "name"

	counterKind     = "counter"
	counterValueKey = "value"
)

type counterContainer struct {
	Kind  string `dynamo:"id"`
	Key   string `dynamo:"name"`
	Value int    `dynamo:"value"`
}

type objectContainer struct {
	Kind string `dynamo:"id"`
	Key  string `dynamo:"name"`
	Item string `dynamo:"item"`
}

type Client interface {
	Put(kind string, key string, object interface{}) error
	Get(kind string, key string) (string, error)

	Inc(key string) (int, error)
	Top(key string) (int, error)
}

type client struct {
	table *dynamo.Table
}

func (c *client) Put(kind string, key string, object interface{}) error {
	b, err := json.Marshal(object)
	if err != nil {
		return err
	}
	return c.table.Put(objectContainer{Kind: kind, Key: key, Item: string(b)}).Run()
}

func (c *client) Get(kind string, key string) (string, error) {
	buf := objectContainer{}
	err := c.table.Get(partKey, kind).Range(sortKey, dynamo.Equal, key).One(&buf)
	return buf.Item, err
}

func (c *client) Inc(key string) (int, error) {
	// ensure exists
	buf := counterContainer{}
	err := c.table.Update(partKey, counterKind).Range(sortKey, key).SetIfNotExists(counterValueKey, 0).Value(&buf)
	if err != nil {
		return -1, err
	}
	err = c.table.Update(partKey, counterKind).Range(sortKey, key).SetExpr("'"+counterValueKey+"' = '"+counterValueKey+"' + ?", 1).Value(&buf)
	return buf.Value, err
}
func (c *client) Top(key string) (int, error) {
	buf := counterContainer{}
	err := c.table.Get(partKey, counterKind).Range(sortKey, dynamo.Equal, key).One(&buf)
	return buf.Value, err
}

func NewClient(region, tableName string) (Client, error) {
	t, err := table(region, tableName)
	if err != nil {
		return nil, err
	}
	return &client{table: t}, nil
}

func table(region, tableName string) (*dynamo.Table, error) {
	cfg := aws.NewConfig().WithRegion(region)
	s, err := session.NewSession()
	if err != nil {
		return nil, err
	}
	db := dynamo.New(s, cfg)
	t := db.Table(tableName)
	return &t, nil
}
