package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Hodik/noteshelf-be.git/repository"
	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/jwt"
	"github.com/clerk/clerk-sdk-go/v2/user"
	"github.com/gin-gonic/gin"
)

// AuthError represents authentication-related errors
type AuthError struct {
	Message    string
	Metadata   string
	StatusCode int
}

func (e AuthError) Error() string {
	return fmt.Sprintf("auth error: %s, metadata: %s", e.Message, e.Metadata)
}

func NewAuthError(message, metadata string, statusCode int) AuthError {
	return AuthError{
		Message:    message,
		Metadata:   metadata,
		StatusCode: statusCode,
	}
}

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
			c.Error(NewAuthError("Authorization header missing", "no auth header provided", http.StatusUnauthorized))
			c.Abort()
			return
		}

		parts := strings.Split(authheader, " ")

		if parts[0] != "Bearer" || len(parts) != 2 {
			c.Error(NewAuthError("Invalid authorization format", "expected 'Bearer <token>'", http.StatusUnauthorized))
			c.Abort()
			return
		}

		claims, err := jwt.Verify(c, &jwt.VerifyParams{
			Token: parts[1],
		})
		if err != nil {
			c.Error(NewAuthError("Invalid JWT token", err.Error(), http.StatusUnauthorized))
			c.Abort()
			return
		}

		usr, err := user.Get(c, claims.Subject)
		if err != nil {
			c.Error(NewAuthError("Failed to get user from Clerk", err.Error(), http.StatusUnauthorized))
			c.Abort()
			return
		}
		c.Set("user", usr)

		dbUser, err := syncUser(c, usr, queries)
		if err != nil {
			// Check if it's an auth error or server error
			if authErr, ok := err.(AuthError); ok {
				c.Error(authErr)
			} else {
				c.Error(err) // Server error
			}
			c.Abort()
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
			return nil, NewAuthError("Email required", "user must have a primary email address", http.StatusBadRequest)
		}

		user, err = queries.CreateUser(ctx, repository.CreateUserParams{
			ID:        usr.ID,
			FirstName: usr.FirstName,
			LastName:  usr.LastName,
			Username:  usr.Username,
			Email:     *email,
			Phone:     phoneNumber,
		})
		if err != nil {
			return nil, err // Database error - let it bubble up as 500
		}
		return &user, nil
	}
	return nil, err // Database error - let it bubble up as 500
}

func GetClerkUserFromRequest(c *gin.Context) (*clerk.User, error) {
	user, exists := c.Get("user")

	if !exists {
		return nil, NewAuthError("User not found in context", "middleware authentication failed", http.StatusInternalServerError)
	}

	clerkUser, ok := user.(*clerk.User)
	if !ok {
		return nil, NewAuthError("Invalid user type in context", "expected Clerk user", http.StatusInternalServerError)
	}

	return clerkUser, nil
}

func GetDBUserFromRequest(c *gin.Context) (*repository.User, error) {
	user, exists := c.Get("dbUser")

	if !exists {
		return nil, NewAuthError("Database user not found in context", "middleware authentication failed", http.StatusInternalServerError)
	}

	dbUser, ok := user.(*repository.User)
	if !ok {
		return nil, NewAuthError("Invalid user type in context", "expected database user", http.StatusInternalServerError)
	}

	return dbUser, nil
}
