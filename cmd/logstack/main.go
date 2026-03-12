package main

import (
	"log"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/kayushkin/logstack/internal/api"
	"github.com/kayushkin/logstack/internal/store"
)

func main() {
	// Get configuration from environment
	port := getEnv("LOGSTACK_PORT", "8081")
	dataDir := getEnv("LOGSTACK_DATA_DIR", "./logs")
	ginMode := getEnv("GIN_MODE", "release")

	// Set gin mode
	gin.SetMode(ginMode)

	// Initialize store
	s, err := store.NewFileStore(dataDir)
	if err != nil {
		log.Fatalf("Failed to initialize store: %v", err)
	}
	log.Printf("Log stack initialized with data dir: %s", dataDir)

	// Create API handler
	h := api.NewHandler(s)

	// Setup router
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	// Setup routes
	h.SetupRoutes(r)

	// Start server
	log.Printf("Log stack listening on :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
