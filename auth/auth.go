package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/Hodik/noteshelf-be.git/repository"
	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/jwt"
	"github.com/clerk/clerk-sdk-go/v2/user"
	"github.com/gin-gonic/gin"
)

func GetPrimaryPhone(usr *clerk.User) *string {
	if usr.PrimaryPhoneNumberID == nil {
		return nil
	} else {
		for _, phone := range usr.PhoneNumbers {
			if phone != nil {
				if phone.ID == *usr.PrimaryPhoneNumberID {
					return &phone.PhoneNumber
				}
			}
		}
	}

	return nil
}

func GetPrimaryEmail(usr *clerk.User) *string {
	if usr.PrimaryEmailAddressID == nil {
		return nil
	} else {
		for _, email := range usr.EmailAddresses {
			if email != nil {
				if email.ID == *usr.PrimaryEmailAddressID {
					return &email.EmailAddress
				}
			}
		}
	}

	return nil
}

func AuthMiddleware(queries *repository.Queries) gin.HandlerFunc {
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

		usr, err := user.Get(c, claims.Subject)
		c.Set("user", usr)

		dbUser, err := syncUser(c, usr, queries)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, &gin.H{"detail": err.Error()})
			return
		}
		c.Set("dbUser", dbUser)
		c.Next()
	}
}

func syncUser(ctx context.Context, usr *clerk.User, queries *repository.Queries) (*repository.User, error) {
	user, err := queries.GetUserById(ctx, usr.ID)

	if err == nil {
		return &user, nil
	}

	if errors.Is(err, sql.ErrNoRows) {
		phoneNumber := GetPrimaryPhone(usr)
		email := GetPrimaryEmail(usr)
		if email == nil {
			return nil, errors.New("email not found in clerk user")
		}

		user, err = queries.CreateUser(ctx, repository.CreateUserParams{ID: usr.ID, FirstName: usr.FirstName, LastName: usr.LastName, Username: usr.Username, Email: *email, Phone: phoneNumber})
		if err != nil {
			return nil, err
		} else {
			return &user, nil

		}
	}
	return nil, err
}

func GetClerkUserFromRequest(c *gin.Context) (*clerk.User, error) {
	user, exists := c.Get("user")

	if !exists {
		c.AbortWithStatus(http.StatusInternalServerError)
		return nil, errors.New("user is not found in context")
	}

	clerkUser, ok := user.(*clerk.User)
	if !ok {
		c.AbortWithStatus(http.StatusInternalServerError)
		return nil, errors.New("user is not clerk user")
	}

	return clerkUser, nil
}

func GetDBUserFromRequest(c *gin.Context) (*repository.User, error) {
	user, exists := c.Get("dbUser")

	if !exists {
		c.AbortWithStatus(http.StatusInternalServerError)
		return nil, errors.New("user is not found in context")
	}

	dbUser, ok := user.(*repository.User)
	if !ok {
		c.AbortWithStatus(http.StatusInternalServerError)
		return nil, errors.New("user is not clerk user")
	}

	return dbUser, nil
}
