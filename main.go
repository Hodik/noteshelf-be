package main

import (
	"net/http"
	"os"
	"strings"

	"fmt"
	"log"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/jwt"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
  "github.com/clerk/clerk-sdk-go/v2/user"
)

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authheader := c.GetHeader("Authorization")
		if authheader == "" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		parts := strings.Split(authheader, " ")

		if parts[0] != "Bearer" || len(parts) != 2 { 
			c.AbortWithStatus(http.StatusNotAcceptable)
			return
		}

    claims, err := jwt.Verify(c, &jwt.VerifyParams{
      Token: parts[1],
    })
    if err != nil {
      c.AbortWithStatus(http.StatusUnauthorized)
      return
    }

    fmt.Println(claims.Subject)

    usr, err := user.Get(c, claims.Subject)
    fmt.Println(usr)
		c.Set("user", claims.Subject)
		c.Next()
	}
}

func main() {
  fmt.Println("here")

  if err := godotenv.Load(); err != nil {
    log.Fatalf("failed to load env variables")
  }
  clerk.SetKey(os.Getenv("CLERK_SECRET_KEY"))

	router := gin.Default()

	router.Use(AuthMiddleware())
	router.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})

	router.Run()
}
