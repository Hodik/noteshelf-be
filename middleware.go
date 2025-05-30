package main

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		for _, err := range c.Errors {
			switch e := err.Err.(type) {
			case Http:
				c.AbortWithStatusJSON(e.StatusCode, e)
			default:
				if strings.Contains(e.Error(), "no rows") {
					c.AbortWithStatusJSON(http.StatusNotFound, map[string]string{"message": e.Error()})
					return
				}
				c.AbortWithStatusJSON(http.StatusInternalServerError, map[string]string{"message": "Service Unavailable"})
			}
		}
	}
}
