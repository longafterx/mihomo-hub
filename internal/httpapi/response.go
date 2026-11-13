package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/longafterx/mihomo-hub/internal/instance"
)

func decode(c *gin.Context, dst any, limit int64) bool {
	d := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, limit))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "invalid JSON body or unknown field")
		return false
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		fail(c, http.StatusBadRequest, "invalid_json", "request must contain exactly one JSON object")
		return false
	}
	return true
}

func serviceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, instance.ErrNotFound):
		fail(c, http.StatusNotFound, "instance_not_found", "instance not found")
	case errors.Is(err, instance.ErrConflict):
		fail(c, http.StatusConflict, "instance_conflict", "instance name already exists")
	case errors.Is(err, instance.ErrInvalid):
		fail(c, http.StatusBadRequest, "invalid_instance", "invalid name, secret, controller URL or history choice")
	default:
		fail(c, http.StatusInternalServerError, "storage_error", "could not persist instance change")
	}
}

func respond(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"data": data, "meta": gin.H{}, "error": nil})
}

func fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{
		"data": nil, "meta": gin.H{}, "error": gin.H{"code": code, "message": message},
	})
}
