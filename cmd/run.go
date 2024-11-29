package main

import (
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gocql/gocql"
)

type Store struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
}

var session *gocql.Session
func getHostIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		log.Println("Error getting network interfaces:", err)
		return "localhost"
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "localhost"
}

func main() {
	// Initialize Cassandra session
	initCassandra()

	defer session.Close()

	// Seed data
	seedData()

	// Initialize Gin router
	router := gin.Default()

	// Store endpoints
	router.POST("/stores", createStore)
	router.GET("/stores/:id", getStore)
	router.PUT("/stores/:id", updateStore)
	router.DELETE("/stores/:id", deleteStore)
	router.GET("/stores", listStores) // Search and list stores

	// Run the server
	router.Run(":8080")
}

// Initialize Cassandra and create the stores table
func initCassandra() {
	// Determine host IP
	cassandraHost := getHostIP()
	log.Printf("Attempting to connect to Cassandra at %s:9042", cassandraHost)

	// First, connect without a keyspace to create it
	defaultCluster := gocql.NewCluster(cassandraHost)
	defaultCluster.Port = 9042
	defaultCluster.Authenticator = gocql.PasswordAuthenticator{
		Username: "cassandra",
		Password: "cassandra",
	}
	defaultCluster.Consistency = gocql.Quorum
	defaultCluster.ConnectTimeout = time.Second * 10

	// Create initial session to create keyspace
	defaultSession, err := defaultCluster.CreateSession()
	if err != nil {
		log.Fatalf("Error creating default Cassandra session: %v", err)
	}
	defer defaultSession.Close()

	// Create keyspace
	err = defaultSession.Query(`CREATE KEYSPACE IF NOT EXISTS store_management 
		WITH REPLICATION = {
			'class': 'SimpleStrategy', 
			'replication_factor': 1
		}`).Exec()
	if err != nil {
		log.Fatalf("Error creating keyspace: %v", err)
	}

	// Now connect with the keyspace
	cluster := gocql.NewCluster(cassandraHost)
	cluster.Keyspace = "store_management"
	cluster.Port = 9042
	cluster.Authenticator = gocql.PasswordAuthenticator{
		Username: "cassandra",
		Password: "cassandra",
	}
	cluster.Consistency = gocql.Quorum
	cluster.ConnectTimeout = time.Second * 10

	session, err = cluster.CreateSession()
	if err != nil {
		log.Fatalf("Error creating Cassandra session with keyspace: %v", err)
	}

	// Create table if it doesn't exist
	err = session.Query(`
		CREATE TABLE IF NOT EXISTS stores (
			id UUID PRIMARY KEY,
			name TEXT,
			location TEXT
		)`).Exec()
	if err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}

	log.Println("Table 'stores' is ready.")
}

// Seed some initial data into the stores table
func seedData() {
	initialStores := []Store{
		{ID: "11111111-1111-1111-1111-111111111111", Name: "Store Alpha", Location: "Downtown"},
		{ID: "22222222-2222-2222-2222-222222222222", Name: "Store Beta", Location: "Uptown"},
		{ID: "33333333-3333-3333-3333-333333333333", Name: "Store Gamma", Location: "Suburbs"},
	}

	for _, store := range initialStores {
		err := session.Query(
			"INSERT INTO stores (id, name, location) VALUES (?, ?, ?)",
			store.ID, store.Name, store.Location,
		).Exec()
		if err != nil {
			log.Printf("Failed to seed store: %v", err)
		} else {
			log.Printf("Seeded store: %v", store.Name)
		}
	}
}

//// CRUD Operations ////

// Create a new store
func createStore(c *gin.Context) {
	var store Store
	if err := c.BindJSON(&store); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// Insert store into Cassandra
	err := session.Query(
		"INSERT INTO stores (id, name, location) VALUES (?, ?, ?)",
		store.ID, store.Name, store.Location,
	).Exec()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create store"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Store created successfully"})
}

// Get a store by ID
func getStore(c *gin.Context) {
	id := c.Param("id")

	var store Store
	err := session.Query(
		"SELECT id, name, location FROM stores WHERE id = ?",
		id,
	).Scan(&store.ID, &store.Name, &store.Location)
	if err != nil {
		if err == gocql.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"message": "Store not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve store"})
		}
		return
	}

	c.JSON(http.StatusOK, store)
}

// Update a store by ID
func updateStore(c *gin.Context) {
	id := c.Param("id")

	var store Store
	if err := c.BindJSON(&store); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// Update store in Cassandra
	err := session.Query(
		"UPDATE stores SET name = ?, location = ? WHERE id = ?",
		store.Name, store.Location, id,
	).Exec()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update store"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Store updated successfully"})
}

// Delete a store by ID
func deleteStore(c *gin.Context) {
	id := c.Param("id")

	// Delete store from Cassandra
	err := session.Query(
		"DELETE FROM stores WHERE id = ?",
		id,
	).Exec()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete store"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Store deleted successfully"})
}

// List stores with optional pagination
func listStores(c *gin.Context) {
	limitParam := c.DefaultQuery("limit", "10")

	// Parse limit
	limit, err := strconv.Atoi(limitParam)
	if err != nil || limit <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid limit"})
		return
	}

	var stores []Store
	iter := session.Query(
		"SELECT id, name, location FROM stores LIMIT ?",
		limit,
	).Iter()

	var store Store
	for iter.Scan(&store.ID, &store.Name, &store.Location) {
		stores = append(stores, store)
	}

	if err := iter.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve stores"})
		return
	}

	c.JSON(http.StatusOK, stores)
}
