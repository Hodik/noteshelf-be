package main

import (
	"context"
	"net/http"
	"os"
	"strings"

	"fmt"
	"log"

	"github.com/Hodik/noteshelf-be.git/repository"
	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/jwt"
	"github.com/clerk/clerk-sdk-go/v2/user"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

var dbPool *pgxpool.Pool

func setupDB() error {
	log.Println("connecting to ", os.Getenv("POSTGRESQL_URL"))
	var err error
	dbPool, err = pgxpool.New(context.Background(), os.Getenv("POSTGRESQL_URL"))

	if err != nil {
		return err
	}
	return nil
}

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

func syncUser(usr *clerk.User) *repository.User {
	return &repository.User{ID: usr.ID, Username: usr.Username, Firstname: usr.FirstName, Lastname: usr.LastName, CreatedAt: usr.CreatedAt, UpadatedAt: usr.UpdatedAt}
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatalf("failed to load env variables")
	}
	clerk.SetKey(os.Getenv("CLERK_SECRET_KEY"))
	if err := setupDB(); err != nil {
		log.Fatalf("failed to setup db connections", err)
	}

	router := gin.Default()

	router.Use(AuthMiddleware())
	router.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})

	router.Run()
}
