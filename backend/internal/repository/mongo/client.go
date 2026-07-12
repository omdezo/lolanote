// Package mongo implements the domain repository interfaces on MongoDB.
// Element ids are 24-hex ObjectIds end to end — the same id shape Milanote
// validates client-side (§9.4).
package mongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

// Store bundles the connected database and hands out typed repositories.
type Store struct {
	Client *mongo.Client
	DB     *mongo.Database
}

// Connect dials MongoDB and pings the primary.
func Connect(ctx context.Context, uri, dbName string) (*Store, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return nil, fmt.Errorf("mongo ping: %w", err)
	}
	return &Store{Client: client, DB: client.Database(dbName)}, nil
}

// Close disconnects the client.
func (s *Store) Close(ctx context.Context) error { return s.Client.Disconnect(ctx) }

// NewID mints a 24-hex ObjectId string.
func NewID() string { return primitive.NewObjectID().Hex() }

// Collection names, in one place.
const (
	colElements      = "elements"
	colTransactions  = "transactions"
	colUsers         = "users"
	colComments      = "comments"
	colLabels        = "labels"
	colAttachments   = "attachments"
	colNotifications = "notifications"
)
