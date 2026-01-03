package main

import (
	"log"
	"vaportrail/internal/config"
	"vaportrail/internal/db"
	"vaportrail/internal/scheduler"
	"vaportrail/internal/web"
)

func main() {
	cfg := config.Load()
	log.Printf("Starting VaporTrail on port %d...", cfg.HTTPPort)
	log.Printf("Using database at %s", cfg.DBPath)

	dbConn, err := db.New(cfg.DBPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	log.Println("Database initialized successfully")
	defer dbConn.Close()

	sched := scheduler.New(dbConn)

	// Add a sample target if none exist
	targets, _ := dbConn.GetTargets()
	if len(targets) == 0 {
		log.Println("Adding sample target: Google")
		_, err := dbConn.AddTarget(&db.Target{
			Name:        "Google",
			Address:     "google.com",
			ProbeType:   "ping",
			ProbeConfig: "",
		})
		if err != nil {
			log.Printf("Failed to add sample target: %v", err)
		}
	}

	if err := sched.Start(); err != nil {
		log.Fatalf("Failed to start scheduler: %v", err)
	}

	// Start Web Server
	ws := web.New(cfg, dbConn, sched)
	go func() {
		if err := ws.Start(); err != nil {
			log.Fatalf("Web server failed: %v", err)
		}
	}()

	select {} // Block forever
}
