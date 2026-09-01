// Package db owns the MongoDB connection.
package db

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	connectAttempts = 10
	connectBackoff  = time.Second
)

// Connect dials MongoDB and pings it until it answers. Compose already gates the
// API on a Mongo healthcheck, but a bounded retry here means a slow-starting
// database never kills the container.
func Connect(ctx context.Context, uri string) (*mongo.Client, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}

	for attempt := 1; attempt <= connectAttempts; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = client.Database("admin").RunCommand(pingCtx, bson.D{{Key: "ping", Value: 1}}).Err()
		cancel()
		if err == nil {
			log.Printf("mongo: connected on attempt %d", attempt)
			return client, nil
		}
		log.Printf("mongo: not ready (attempt %d/%d): %v", attempt, connectAttempts, err)

		select {
		case <-ctx.Done():
			_ = client.Disconnect(context.Background())
			return nil, ctx.Err()
		case <-time.After(connectBackoff):
		}
	}

	_ = client.Disconnect(context.Background())
	return nil, fmt.Errorf("mongo unreachable after %d attempts: %w", connectAttempts, err)
}
