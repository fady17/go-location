package main

import (
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gocql/gocql"
)

type store struct {
	ID       int    `json:"id"`
	AreaID   int    `json:"areaId"`
	Name     string `json:"name"`
	Location string `json:"location"`
}

var session *gocql.Session

// getHostIP attempts to get the non-loopback IP address
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

// optimizedStoreSearch performs an optimized search based on areaID, name, and location
func optimizedStoreSearch(areaID int, name, location string) ([]store, error) {
	var stores []store

	// Prepare the query with flexible criteria
	query := "SELECT id, area_id, name, location FROM stores WHERE 1=1"
	var args []interface{}

	if areaID >= 0 {
		query += " AND area_id = ?"
		args = append(args, areaID)
	}
	if name != "" {
		query += " AND name LIKE ?"
		args = append(args, "%"+name+"%")
	}
	if location != "" {
		query += " AND location LIKE ?"
		args = append(args, "%"+location+"%")
	}

	iter := session.Query(query, args...).Iter()
	var s store
	for iter.Scan(&s.ID, &s.AreaID, &s.Name, &s.Location) {
		stores = append(stores, s)
	}

	if err := iter.Close(); err != nil {
		return nil, err
	}

	return stores, nil
}

// Implement batch processing for writes
func batchStoreInsert(stores []store) error {
	batch := session.NewBatch(gocql.LoggedBatch)

	for _, s := range stores {
		batch.Query(`INSERT INTO stores (id, area_id, name, location) 
			VALUES (?, ?, ?, ?)`,
			s.ID, s.AreaID, s.Name, s.Location)
	}

	// Execute batch with consistency level
	err := session.ExecuteBatch(batch)
	return err
}

// parallelStoreSearch searches for stores concurrently based on multiple criteria
func parallelStoreSearch(searchParams []searchCriteria) ([]store, error) {
	var results []store
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Process each search criteria concurrently
	for _, params := range searchParams {
		wg.Add(1)
		go func(p searchCriteria) {
			defer wg.Done()

			// Perform the search
			searchResults, err := optimizedStoreSearch(p.AreaID, p.Name, p.Location)
			if err == nil {
				// Lock to append results safely across goroutines
				mu.Lock()
				results = append(results, searchResults...)
				mu.Unlock()
			}
		}(params)
	}

	// Wait for all goroutines to complete
	wg.Wait()
	return results, nil
}

// Search criteria struct for flexible searching
type searchCriteria struct {
	AreaID   int
	Name     string
	Location string
}

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

	// Create table
	err = session.Query(`CREATE TABLE IF NOT EXISTS stores (
		id int PRIMARY KEY,
		area_id int,
		name text,
		location text
	)`).Exec()
	if err != nil {
		log.Fatalf("Error creating stores table: %v", err)
	}
}

func ResponseTimeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		// Process request
		c.Next()
		duration := time.Since(start)
		log.Printf("Path: %s, Method: %s, Status: %d, Response Time: %v",
			c.Request.URL.Path,
			c.Request.Method,
			c.Writer.Status(),
			duration)
	}
}

func getStores(c *gin.Context) {
	var stores []store
	iter := session.Query("SELECT id, area_id, name, location FROM stores").Iter()
	
	var s store
	for iter.Scan(&s.ID, &s.AreaID, &s.Name, &s.Location) {
		stores = append(stores, s)
	}

	if err := iter.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.IndentedJSON(http.StatusOK, stores)
}

func getStoreByID(c *gin.Context) {
	idParam := c.Param("id")
	id, err := strconv.Atoi(idParam)
	if err != nil {
		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": "invalid id"})
		return
	}

	var s store
	err = session.Query("SELECT id, area_id, name, location FROM stores WHERE id = ?", id).Scan(
		&s.ID, &s.AreaID, &s.Name, &s.Location,
	)
	
	if err != nil {
		if err == gocql.ErrNotFound {
			c.IndentedJSON(http.StatusNotFound, gin.H{"message": "store not found"})
		} else {
			c.IndentedJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	c.IndentedJSON(http.StatusOK, s)
}

func getStoresByAreaID(c *gin.Context) {
	areaIDParam := c.Param("areaid")
	areaID, err := strconv.Atoi(areaIDParam)
	if err != nil {
		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": "invalid area ID"})
		return
	}

	// Use optimizedStoreSearch to retrieve stores for a specific area
	stores, err := optimizedStoreSearch(areaID, "", "")
	if err != nil {
		c.IndentedJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.IndentedJSON(http.StatusOK, stores)
}

func postStores(c *gin.Context) {
	var newStores []store
	if err := c.BindJSON(&newStores); err != nil {
		c.IndentedJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Insert stores in bulk using batchStoreInsert
	err := batchStoreInsert(newStores)
	if err != nil {
		c.IndentedJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.IndentedJSON(http.StatusCreated, newStores)
}

func searchStores(c *gin.Context) {
	areaIDParam := c.DefaultQuery("areaid", "-1")
	name := c.DefaultQuery("name", "")
	location := c.DefaultQuery("location", "")

	areaID, err := strconv.Atoi(areaIDParam)
	if err != nil {
		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": "invalid area ID"})
		return
	}

	// Perform parallel search
	searchParams := []searchCriteria{
		{AreaID: areaID, Name: name, Location: location},
	}
	stores, err := parallelStoreSearch(searchParams)
	if err != nil {
		c.IndentedJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if len(stores) == 0 {
		c.IndentedJSON(http.StatusNotFound, gin.H{"message": "no stores found"})
	} else {
		c.IndentedJSON(http.StatusOK, stores)
	}
}

func main() {
	// Initialize Cassandra connection
	initCassandra()
	defer session.Close()

	// Seed some initial data if the database is empty
	r := gin.Default()
	r.Use(ResponseTimeMiddleware())

	// API Routes
	r.GET("/stores", getStores)
	r.GET("/stores/:id", getStoreByID)
	r.GET("/stores/area/:areaid", getStoresByAreaID)
	r.POST("/stores", postStores)
	r.GET("/stores/search", searchStores)

	// Start the server
	r.Run(":8080")
}






// package main

// import (
// 	"log"
// 	"net"
// 	"net/http"
// 	"strconv"
// 	"time"

// 	"github.com/gin-gonic/gin"
// 	"github.com/gocql/gocql"
// )

// type Store struct {
// 	ID       int    `json:"id" binding:"required"`
// 	AreaID   int    `json:"areaId" binding:"required"`
// 	Name     string `json:"name" binding:"required"`
// 	Location string `json:"location" binding:"required"`
// }

// var session *gocql.Session

// // getHostIP attempts to get the non-loopback IP address
// func getHostIP() string {
// 	addrs, err := net.InterfaceAddrs()
// 	if err != nil {
// 		log.Println("Error getting network interfaces:", err)
// 		return "localhost"
// 	}
// 	for _, addr := range addrs {
// 		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
// 			if ipnet.IP.To4() != nil {
// 				return ipnet.IP.String()
// 			}
// 		}
// 	}
// 	return "localhost"
// }

// // initCassandra sets up the Cassandra database connection and keyspace
// func initCassandra() {
// 	cassandraHost := getHostIP()
// 	log.Printf("Attempting to connect to Cassandra at %s:9042", cassandraHost)

// 	cluster := gocql.NewCluster(cassandraHost)
// 	cluster.Port = 9042
// 	cluster.Keyspace = "store_management"
// 	cluster.Authenticator = gocql.PasswordAuthenticator{
// 		Username: "cassandra",
// 		Password: "cassandra",
// 	}
// 	cluster.Consistency = gocql.Quorum
// 	cluster.ConnectTimeout = time.Second * 10

// 	var err error
// 	session, err = cluster.CreateSession()
// 	if err != nil {
// 		log.Fatalf("Error creating Cassandra session: %v", err)
// 	}

// 	// Create table with improved schema
// 	err = session.Query(`CREATE TABLE IF NOT EXISTS stores (
// 		id int,
// 		area_id int,
// 		name text,
// 		location text,
// 		PRIMARY KEY ((area_id), id)
// 	) WITH CLUSTERING ORDER BY (id ASC)`).Exec()
// 	if err != nil {
// 		log.Fatalf("Error creating stores table: %v", err)
// 	}
// }

// // Middleware to log response times
// func responseTimeMiddleware() gin.HandlerFunc {
// 	return func(c *gin.Context) {
// 		start := time.Now()
// 		c.Next()
// 		duration := time.Since(start)
// 		log.Printf("Path: %s, Method: %s, Status: %d, Response Time: %v",
// 			c.Request.URL.Path,
// 			c.Request.Method,
// 			c.Writer.Status(),
// 			duration)
// 	}
// }

// // getAllStores retrieves all stores with optional pagination
// func getAllStores(c *gin.Context) {
// 	// Pagination parameters
// 	page := c.DefaultQuery("page", "1")
// 	pageSize := c.DefaultQuery("size", "10")

// 	pageNum, err := strconv.Atoi(page)
// 	if err != nil || pageNum < 1 {
// 		pageNum = 1
// 	}

// 	pageSizeNum, err := strconv.Atoi(pageSize)
// 	if err != nil || pageSizeNum < 1 || pageSizeNum > 100 {
// 		pageSizeNum = 10
// 	}

// 	// Calculate offset
// 	offset := (pageNum - 1) * pageSizeNum

// 	var stores []Store
// 	query := "SELECT id, area_id, name, location FROM stores LIMIT ? OFFSET ?"
// 	iter := session.Query(query, pageSizeNum, offset).Iter()

// 	var s Store
// 	for iter.Scan(&s.ID, &s.AreaID, &s.Name, &s.Location) {
// 		stores = append(stores, s)
// 	}

// 	if err := iter.Close(); err != nil {
// 		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
// 		return
// 	}

// 	c.JSON(http.StatusOK, gin.H{
// 		"stores":     stores,
// 		"page":       pageNum,
// 		"page_size":  pageSizeNum,
// 		"total":      len(stores),
// 	})
// }

// // getStoreByID retrieves a specific store by its ID
// func getStoreByID(c *gin.Context) {
// 	idParam := c.Param("id")
// 	id, err := strconv.Atoi(idParam)
// 	if err != nil {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid store ID"})
// 		return
// 	}

// 	// Get area_id from query parameter (required for partition key)
// 	areaID, err := strconv.Atoi(c.DefaultQuery("area_id", "0"))
// 	if err != nil {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid area ID"})
// 		return
// 	}

// 	var s Store
// 	err = session.Query("SELECT id, area_id, name, location FROM stores WHERE area_id = ? AND id = ?",
// 		areaID, id).Scan(&s.ID, &s.AreaID, &s.Name, &s.Location)

// 	if err != nil {
// 		if err == gocql.ErrNotFound {
// 			c.JSON(http.StatusNotFound, gin.H{"error": "Store not found"})
// 		} else {
// 			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
// 		}
// 		return
// 	}

// 	c.JSON(http.StatusOK, s)
// }

// // getStoresByAreaID retrieves stores for a specific area
// func getStoresByAreaID(c *gin.Context) {
// 	areaIDParam := c.Param("area_id")
// 	areaID, err := strconv.Atoi(areaIDParam)
// 	if err != nil {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid area ID"})
// 		return
// 	}

// 	// Pagination parameters
// 	page := c.DefaultQuery("page", "1")
// 	pageSize := c.DefaultQuery("size", "10")

// 	pageNum, err := strconv.Atoi(page)
// 	if err != nil || pageNum < 1 {
// 		pageNum = 1
// 	}

// 	pageSizeNum, err := strconv.Atoi(pageSize)
// 	if err != nil || pageSizeNum < 1 || pageSizeNum > 100 {
// 		pageSizeNum = 10
// 	}

// 	// Calculate offset
// 	offset := (pageNum - 1) * pageSizeNum

// 	var stores []Store
// 	query := "SELECT id, area_id, name, location FROM stores WHERE area_id = ? LIMIT ? OFFSET ?  ALLOW FILTERING"
// 	iter := session.Query(query, areaID, pageSizeNum, offset).Iter()

// 	var s Store
// 	for iter.Scan(&s.ID, &s.AreaID, &s.Name, &s.Location) {
// 		stores = append(stores, s)
// 	}

// 	if err := iter.Close(); err != nil {
// 		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
// 		return
// 	}

// 	if len(stores) == 0 {
// 		c.JSON(http.StatusNotFound, gin.H{"error": "No stores found for this area"})
// 		return
// 	}

// 	c.JSON(http.StatusOK, gin.H{
// 		"stores":     stores,
// 		"page":       pageNum,
// 		"page_size":  pageSizeNum,
// 		"total":      len(stores),
// 	})
// }

// // createStore adds a new store
// func createStore(c *gin.Context) {
// 	var newStore Store
// 	if err := c.ShouldBindJSON(&newStore); err != nil {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
// 		return
// 	}

// 	// Check if store with same ID already exists
// 	var existingStore Store
// 	err := session.Query("SELECT id FROM stores WHERE area_id = ? AND id = ?",
// 		newStore.AreaID, newStore.ID).Scan(&existingStore.ID)

// 	if err == nil {
// 		c.JSON(http.StatusConflict, gin.H{"error": "Store with this ID already exists in the area"})
// 		return
// 	}

// 	// Insert the new store into Cassandra
// 	err = session.Query(`INSERT INTO stores (id, area_id, name, location)
// 		VALUES (?, ?, ?, ?)`,
// 		newStore.ID, newStore.AreaID, newStore.Name, newStore.Location).Exec()

// 	if err != nil {
// 		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
// 		return
// 	}

// 	c.JSON(http.StatusCreated, newStore)
// }

// // updateStore updates an existing store
// func updateStore(c *gin.Context) {
// 	idParam := c.Param("id")
// 	id, err := strconv.Atoi(idParam)
// 	if err != nil {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid store ID"})
// 		return
// 	}

// 	var updateStore Store
// 	if err := c.ShouldBindJSON(&updateStore); err != nil {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
// 		return
// 	}

// 	// Ensure ID in path matches body
// 	if id != updateStore.ID {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": "ID mismatch"})
// 		return
// 	}

// 	// Check if store exists first
// 	var existingStore Store
// 	err = session.Query("SELECT id FROM stores WHERE area_id = ? AND id = ?",
// 		updateStore.AreaID, id).Scan(&existingStore.ID)

// 	if err != nil {
// 		if err == gocql.ErrNotFound {
// 			c.JSON(http.StatusNotFound, gin.H{"error": "Store not found"})
// 		} else {
// 			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
// 		}
// 		return
// 	}

// 	// Update the store
// 	err = session.Query(`UPDATE stores SET name = ?, location = ?
// 		WHERE area_id = ? AND id = ?`,
// 		updateStore.Name, updateStore.Location,
// 		updateStore.AreaID, id).Exec()

// 	if err != nil {
// 		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
// 		return
// 	}

// 	c.JSON(http.StatusOK, updateStore)
// }

// // deleteStore removes a store
// func deleteStore(c *gin.Context) {
// 	idParam := c.Param("id")
// 	id, err := strconv.Atoi(idParam)
// 	if err != nil {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid store ID"})
// 		return
// 	}

// 	// Get area_id from query parameter (required for partition key)
// 	areaID, err := strconv.Atoi(c.DefaultQuery("area_id", "0"))
// 	if err != nil {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid area ID"})
// 		return
// 	}

// 	// Delete the store
// 	err = session.Query("DELETE FROM stores WHERE area_id = ? AND id = ?",
// 		areaID, id).Exec()

// 	if err != nil {
// 		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
// 		return
// 	}

// 	c.JSON(http.StatusOK, gin.H{"message": "Store deleted successfully"})
// }

// func main() {
// 	// Initialize Cassandra connection
// 	initCassandra()
// 	defer session.Close()

// 	// Setup Gin router
// 	router := gin.Default()

// 	// Add middleware
// 	router.Use(responseTimeMiddleware())

// 	// Define routes with improved REST conventions
// 	router.GET("/stores", getAllStores)
// 	router.GET("/stores/:id", getStoreByID)
// 	router.GET("/areas/:area_id/stores", getStoresByAreaID)
// 	router.POST("/stores", createStore)
// 	router.PUT("/stores/:id", updateStore)
// 	router.DELETE("/stores/:id", deleteStore)

// 	// Run the server
// 	router.Run("localhost:8080")
// }

// package main

// import (
// 	"log"
// 	"net"
// 	"net/http"
// 	"strconv"
// 	"time"

// 	"sync"

// 	"github.com/gin-gonic/gin"
// 	"github.com/gocql/gocql"
// )

// type store struct {
// 	ID       int    `json:"id"`
// 	AreaID   int    `json:"areaId"`
// 	Name     string `json:"name"`
// 	Location string `json:"location"`
// }

// var session *gocql.Session

// // getHostIP attempts to get the non-loopback IP address
// func getHostIP() string {
// 	addrs, err := net.InterfaceAddrs()
// 	if err != nil {
// 		log.Println("Error getting network interfaces:", err)
// 		return "localhost"
// 	}
// 	for _, addr := range addrs {
// 		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
// 			if ipnet.IP.To4() != nil {
// 				return ipnet.IP.String()
// 			}
// 		}
// 	}
// 	return "localhost"
// }

// // optimizedStoreSearch performs an optimized search based on areaID, name, and location
// func optimizedStoreSearch(areaID int, name, location string) ([]store, error) {
// 	var stores []store

// 	// Prepare the query with flexible criteria
// 	query := "SELECT id, area_id, name, location FROM stores WHERE 1=1"
// 	var args []interface{}

// 	if areaID >= 0 {
// 		query += " AND area_id = ?"
// 		args = append(args, areaID)
// 	}
// 	if name != "" {
// 		query += " AND name LIKE ?"
// 		args = append(args, "%"+name+"%")
// 	}
// 	if location != "" {
// 		query += " AND location LIKE ?"
// 		args = append(args, "%"+location+"%")
// 	}

// 	iter := session.Query(query, args...).Iter()
// 	var s store
// 	for iter.Scan(&s.ID, &s.AreaID, &s.Name, &s.Location) {
// 		stores = append(stores, s)
// 	}

// 	if err := iter.Close(); err != nil {
// 		return nil, err
// 	}

// 	return stores, nil
// }


// // Implement batch processing for writes
// func batchStoreInsert(stores []store) error {
// 	batch := session.NewBatch(gocql.LoggedBatch)

// 	for _, s := range stores {
// 		batch.Query(`INSERT INTO stores (id, area_id, name, location) 
// 			VALUES (?, ?, ?, ?)`,
// 			s.ID, s.AreaID, s.Name, s.Location)
// 	}

// 	// Execute batch with consistency level
// 	err := session.ExecuteBatch(batch)
// 	return err
// }

// // parallelStoreSearch searches for stores concurrently based on multiple criteria
// func parallelStoreSearch(searchParams []searchCriteria) ([]store, error) {
// 	var results []store
// 	var mu sync.Mutex
// 	var wg sync.WaitGroup

// 	// Process each search criteria concurrently
// 	for _, params := range searchParams {
// 		wg.Add(1)
// 		go func(p searchCriteria) {
// 			defer wg.Done()

// 			// Perform the search
// 			searchResults, err := optimizedStoreSearch(p.AreaID, p.Name, p.Location)
// 			if err == nil {
// 				// Lock to append results safely across goroutines
// 				mu.Lock()
// 				results = append(results, searchResults...)
// 				mu.Unlock()
// 			}
// 		}(params)
// 	}

// 	// Wait for all goroutines to complete
// 	wg.Wait()
// 	return results, nil
// }

// // Search criteria struct for flexible searching
// type searchCriteria struct {
// 	AreaID   int
// 	Name     string
// 	Location string
// }



// func initCassandra() {
// 	// Determine host IP
// 	cassandraHost := getHostIP()
// 	log.Printf("Attempting to connect to Cassandra at %s:9042", cassandraHost)

// 	// First, connect without a keyspace to create it
// 	defaultCluster := gocql.NewCluster(cassandraHost)
// 	defaultCluster.Port = 9042
// 	defaultCluster.Authenticator = gocql.PasswordAuthenticator{
// 		Username: "cassandra",
// 		Password: "cassandra",
// 	}
// 	defaultCluster.Consistency = gocql.Quorum
// 	defaultCluster.ConnectTimeout = time.Second * 10

// 	// Create initial session to create keyspace
// 	defaultSession, err := defaultCluster.CreateSession()
// 	if err != nil {
// 		log.Fatalf("Error creating default Cassandra session: %v", err)
// 	}
// 	defer defaultSession.Close()

// 	// Create keyspace
// 	err = defaultSession.Query(`CREATE KEYSPACE IF NOT EXISTS store_management 
// 		WITH REPLICATION = {
// 			'class': 'SimpleStrategy', 
// 			'replication_factor': 1
// 		}`).Exec()
// 	if err != nil {
// 		log.Fatalf("Error creating keyspace: %v", err)
// 	}

// 	// Now connect with the keyspace
// 	cluster := gocql.NewCluster(cassandraHost)
// 	cluster.Keyspace = "store_management"
// 	cluster.Port = 9042
// 	cluster.Authenticator = gocql.PasswordAuthenticator{
// 		Username: "cassandra",
// 		Password: "cassandra",
// 	}
// 	cluster.Consistency = gocql.Quorum
// 	cluster.ConnectTimeout = time.Second * 10

// 	session, err = cluster.CreateSession()
// 	if err != nil {
// 		log.Fatalf("Error creating Cassandra session with keyspace: %v", err)
// 	}

// 	// Create table
// 	err = session.Query(`CREATE TABLE IF NOT EXISTS stores (
// 		id int PRIMARY KEY,
// 		area_id int,
// 		name text,
// 		location text
// 	)`).Exec()
// 	if err != nil {
// 		log.Fatalf("Error creating stores table: %v", err)
// 	}
// }

// func ResponseTimeMiddleware() gin.HandlerFunc {
// 	return func(c *gin.Context) {
// 		start := time.Now()
// 		// Process request
// 		c.Next()
// 		duration := time.Since(start)
// 		log.Printf("Path: %s, Method: %s, Status: %d, Response Time: %v",
// 			c.Request.URL.Path,
// 			c.Request.Method,
// 			c.Writer.Status(),
// 			duration)
// 	}
// }

// func getStores(c *gin.Context) {
// 	var stores []store
// 	iter := session.Query("SELECT id, area_id, name, location FROM stores").Iter()
	
// 	var s store
// 	for iter.Scan(&s.ID, &s.AreaID, &s.Name, &s.Location) {
// 		stores = append(stores, s)
// 	}

// 	if err := iter.Close(); err != nil {
// 		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
// 		return
// 	}

// 	c.IndentedJSON(http.StatusOK, stores)
// }

// func getStoreByID(c *gin.Context) {
// 	idParam := c.Param("id")
// 	id, err := strconv.Atoi(idParam)
// 	if err != nil {
// 		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": "invalid id"})
// 		return
// 	}

// 	var s store
// 	err = session.Query("SELECT id, area_id, name, location FROM stores WHERE id = ?", id).Scan(
// 		&s.ID, &s.AreaID, &s.Name, &s.Location,
// 	)
	
// 	if err != nil {
// 		if err == gocql.ErrNotFound {
// 			c.IndentedJSON(http.StatusNotFound, gin.H{"message": "store not found"})
// 		} else {
// 			c.IndentedJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
// 		}
// 		return
// 	}

// 	c.IndentedJSON(http.StatusOK, s)
// }

// func getStoresByAreaID(c *gin.Context) {
// 	areaIDParam := c.Param("areaid")
// 	areaID, err := strconv.Atoi(areaIDParam)
// 	if err != nil {
// 		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": "invalid area ID"})
// 		return
// 	}

// 	var stores []store
// 	iter := session.Query("SELECT id, area_id, name, location FROM stores WHERE area_id = ?", areaID).Iter()
	
// 	var s store
// 	for iter.Scan(&s.ID, &s.AreaID, &s.Name, &s.Location) {
// 		stores = append(stores, s)
// 	}

// 	if err := iter.Close(); err != nil {
// 		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
// 		return
// 	}

// 	if len(stores) > 0 {
// 		c.IndentedJSON(http.StatusOK, stores)
// 	} else {
// 		c.IndentedJSON(http.StatusNotFound, gin.H{"message": "no stores found for this area ID"})
// 	}
// }

// func postStores(c *gin.Context) {
// 	var newStore store
// 	if err := c.BindJSON(&newStore); err != nil {
// 		c.IndentedJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
// 		return
// 	}

// 	// Insert the new store into Cassandra
// 	err := session.Query(`INSERT INTO stores (id, area_id, name, location) 
// 		VALUES (?, ?, ?, ?)`,
// 		newStore.ID, newStore.AreaID, newStore.Name, newStore.Location).Exec()
	
// 	if err != nil {
// 		c.IndentedJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
// 		return
// 	}

// 	c.IndentedJSON(http.StatusCreated, newStore)
// }

// func main() {
// 	// Initialize Cassandra connection
// 	initCassandra()
// 	defer session.Close()

// 	// Seed some initial data if the table is empty
// 	var count int
// 	err := session.Query("SELECT COUNT(*) FROM stores").Scan(&count)
// 	if err != nil {
// 		log.Fatalf("Error checking store count: %v", err)
// 	}

// 	if count == 0 {
// 		// Insert initial stores if no data exists
// 		initialStores := []store{
// 			{ID: 0, AreaID: 0, Name: "store0", Location: "h10"},
// 			{ID: 1, AreaID: 0, Name: "store1", Location: "h11"},
// 			{ID: 2, AreaID: 1, Name: "store2", Location: "h12"},
// 			{ID: 3, AreaID: 1, Name: "store3", Location: "h13"},
// 		}

// 		for _, s := range initialStores {
// 			err := session.Query(`INSERT INTO stores (id, area_id, name, location) 
// 				VALUES (?, ?, ?, ?)`,
// 				s.ID, s.AreaID, s.Name, s.Location).Exec()
// 			if err != nil {
// 				log.Printf("Error inserting initial store: %v", err)
// 			}
// 		}
// 	}

// 	// Setup Gin router
// 	router := gin.Default()
// 	router.Use(ResponseTimeMiddleware())

// 	// Define routes
// 	router.GET("/stores", getStores)
// 	router.GET("/stores/:id", getStoreByID)
// 	router.GET("/stores/area/:areaid", getStoresByAreaID)
// 	router.POST("/stores", postStores)

// 	// Run the server
// 	router.Run("localhost:8080")
// }

// package main

// import (
// 	"fmt"
// 	"log"
// 	"net/http"
// 	"strconv"
// 	"time"

// 	"github.com/gin-gonic/gin"
// 	"github.com/gocql/gocql"
// )

// type store struct {
// 	ID       int    `json:"id"`
// 	AreaID   int    `json:"areaId"`
// 	Name     string `json:"name"`
// 	Location string `json:"location"`
// }

// // MOCK
// var stores = []store{
// 	{ID: 0, AreaID: 0, Name: "store0", Location: "h10"},
// 	{ID: 1, AreaID: 0, Name: "store1", Location: "h11"},
// 	{ID: 2, AreaID: 1, Name: "store2", Location: "h12"},
// 	{ID: 3, AreaID: 1, Name: "store3", Location: "h13"},
// }

// func ResponseTimeMiddleware() gin.HandlerFunc {
// 	return func(c *gin.Context) {
		
// 		start := time.Now()

// 		// Process request
// 		c.Next()

		
// 		duration := time.Since(start)

		
// 		log.Printf("Path: %s, Method: %s, Status: %d, Response Time: %v", 
// 			c.Request.URL.Path, 
// 			c.Request.Method, 
// 			c.Writer.Status(), 
// 			duration)
// 	}
// }

// func main() {

// 	cluster := gocql.NewCluster("192.168.1.1", "192.168.1.2", "192.168.1.3")
//     cluster.Keyspace = "example"
//     cluster.Consistency = gocql.Quorum
//     session, _ := cluster.CreateSession()
//     defer session.Close()
 
//     if err := session.Query(`INSERT INTO tweet (timeline, id, text) VALUES (?, ?, ?)`,
//         "me", gocql.TimeUUID(), "hello world").Exec(); err != nil {
//         log.Fatal(err)
//     }
 
//     var id gocql.UUID
//     var text string
 
//     if err := session.Query(`SELECT id, text FROM tweet WHERE timeline = ? LIMIT 1`,
//         "me").Consistency(gocql.One).Scan(&id, &text); err != nil {
//         log.Fatal(err)
//     }
//     fmt.Println("Tweet:", id, text)
 
//     iter := session.Query(`SELECT id, text FROM tweet WHERE timeline = ?`, "me").Iter()
//     for iter.Scan(&id, &text) {
//         fmt.Println("Tweet:", id, text)
//     }
//     if err := iter.Close(); err != nil {
//         log.Fatal(err)
//     }
// }

// 	router := gin.Default()
// 	router.Use(ResponseTimeMiddleware())

// 	router.GET("/stores", getStores)
// 	router.GET("/stores/:id", getStoreByID)
// 	router.GET("/stores/area/:areaid", getStoresByAreaID)
// 	router.POST("/stores", postStores)
// 	router.Run("localhost:8080")
// }

// // getStoresByAreaID locates stores with matching area ID
// func getStoresByAreaID(c *gin.Context) {
// 	// Convert the area ID parameter to an integer
// 	areaIDParam := c.Param("areaid")
// 	areaID, err := strconv.Atoi(areaIDParam)
// 	if err != nil {
// 		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": "invalid area ID"})
// 		return
// 	}

// 	// Filter stores by area ID
// 	var areaStores []store
// 	for _, a := range stores {
// 		if a.AreaID == areaID {
// 			areaStores = append(areaStores, a)
// 		}
// 	}

// 	// Return filtered stores or 404 if none found
// 	if len(areaStores) > 0 {
// 		c.IndentedJSON(http.StatusOK, areaStores)
// 	} else {
// 		c.IndentedJSON(http.StatusNotFound, gin.H{"message": "no stores found for this area ID"})
// 	}
// }

// // getStores responds with the list of all stores as JSON
// func getStores(c *gin.Context) {
// 	c.IndentedJSON(http.StatusOK, stores)
// }

// // postStores adds a store from JSON received in the request body.
// func postStores(c *gin.Context) {
// 	var newStore store
// 	// Call BindJSON to bind the received JSON to newStore.
// 	if err := c.BindJSON(&newStore); err != nil {
// 		return
// 	}
// 	// Add the new store to the slice.
// 	stores = append(stores, newStore)
// 	c.IndentedJSON(http.StatusCreated, newStore)
// }

// // getStoreByID locates the store whose ID value matches the id
// // parameter sent by the client, then returns that store as a response.
// func getStoreByID(c *gin.Context) {
	
// 	idParam := c.Param("id")
// 	id, err := strconv.Atoi(idParam)
// 	if err != nil {
// 		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": "invalid id"})
// 		return
// 	}

// 	// Loop through the list of stores, looking for
// 	// a store whose ID value matches the parameter.
// 	for _, a := range stores {
// 		if a.ID == id {
// 			c.IndentedJSON(http.StatusOK, a)
// 			return
// 		}
// 	}
// 	c.IndentedJSON(http.StatusNotFound, gin.H{"message": "store not found"})
// }

