package main

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

type store struct {
	ID       int    `json:"id"`
	AreaID   int    `json:"areaId"`
	Name     string `json:"name"`
	Location string `json:"location"`
}

// MOCK
var stores = []store{
	{ID: 0, AreaID: 0, Name: "store0", Location: "h10"},
	{ID: 1, AreaID: 0, Name: "store1", Location: "h11"},
	{ID: 2, AreaID: 1, Name: "store2", Location: "h12"},
	{ID: 3, AreaID: 1, Name: "store3", Location: "h13"},
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

func main() {
	router := gin.Default()
	router.Use(ResponseTimeMiddleware())

	router.GET("/stores", getStores)
	router.GET("/stores/:id", getStoreByID)
	router.GET("/stores/area/:areaid", getStoresByAreaID)
	router.POST("/stores", postStores)
	router.Run("localhost:8080")
}

// getStoresByAreaID locates stores with matching area ID
func getStoresByAreaID(c *gin.Context) {
	// Convert the area ID parameter to an integer
	areaIDParam := c.Param("areaid")
	areaID, err := strconv.Atoi(areaIDParam)
	if err != nil {
		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": "invalid area ID"})
		return
	}

	// Filter stores by area ID
	var areaStores []store
	for _, a := range stores {
		if a.AreaID == areaID {
			areaStores = append(areaStores, a)
		}
	}

	// Return filtered stores or 404 if none found
	if len(areaStores) > 0 {
		c.IndentedJSON(http.StatusOK, areaStores)
	} else {
		c.IndentedJSON(http.StatusNotFound, gin.H{"message": "no stores found for this area ID"})
	}
}


// getStores responds with the list of all stores as JSON
func getStores(c *gin.Context) {
	c.IndentedJSON(http.StatusOK, stores)
}

// postStores adds a store from JSON received in the request body.
func postStores(c *gin.Context) {
	var newStore store
	// Call BindJSON to bind the received JSON to newStore.
	if err := c.BindJSON(&newStore); err != nil {
		return
	}
	// Add the new store to the slice.
	stores = append(stores, newStore)
	c.IndentedJSON(http.StatusCreated, newStore)
}

// getStoreByID locates the store whose ID value matches the id
// parameter sent by the client, then returns that store as a response.
func getStoreByID(c *gin.Context) {
	
	idParam := c.Param("id")
	id, err := strconv.Atoi(idParam)
	if err != nil {
		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": "invalid id"})
		return
	}

	// Loop through the list of stores, looking for
	// a store whose ID value matches the parameter.
	for _, a := range stores {
		if a.ID == id {
			c.IndentedJSON(http.StatusOK, a)
			return
		}
	}
	c.IndentedJSON(http.StatusNotFound, gin.H{"message": "store not found"})
}

