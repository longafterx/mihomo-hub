package httpapi

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/longafterx/mihomo-hub/internal/instance"
)

func (a *api) list(c *gin.Context) {
	items := a.Instances.List()
	for i := range items {
		a.withStatus(&items[i])
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	respond(c, http.StatusOK, items)
}

func (a *api) listRetained(c *gin.Context) {
	items, err := a.Instances.ListRetained(c.Request.Context())
	if err != nil {
		serviceError(c, err)
		return
	}
	respond(c, http.StatusOK, items)
}

func (a *api) get(c *gin.Context) {
	item, err := a.Instances.Get(c.Param("id"))
	if err != nil {
		serviceError(c, err)
		return
	}
	a.withStatus(&item)
	respond(c, http.StatusOK, item)
}

func (a *api) editConfig(c *gin.Context) {
	item, err := a.Instances.Get(c.Param("id"))
	if err != nil {
		serviceError(c, err)
		return
	}
	// 管理员编辑时按需读取地址；列表、运行 Endpoint 与响应中均不含 Secret。
	respond(c, http.StatusOK, gin.H{"controller_url": item.ControllerURL})
}

func (a *api) withStatus(item *instance.Instance) {
	s := a.Collector.Status(item.ID, item.Generation)
	item.CollectorStatus = s.Status
	item.LastSeenAtMS = s.LastSeenAtMS
}

func (a *api) status(c *gin.Context) {
	item, err := a.Instances.Get(c.Param("id"))
	if err != nil {
		serviceError(c, err)
		return
	}
	respond(c, 200, a.Collector.Status(item.ID, item.Generation))
}

func (a *api) create(c *gin.Context) {
	var input instance.Create
	if !decode(c, &input, a.Config.Gateway.MaxBodyBytes) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	item, err := a.Instances.Create(ctx, input)
	if err != nil {
		serviceError(c, err)
		return
	}
	a.withStatus(&item)
	respond(c, http.StatusCreated, item)
}

func (a *api) update(c *gin.Context) {
	var input instance.Patch
	if !decode(c, &input, a.Config.Gateway.MaxBodyBytes) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	item, err := a.Instances.Update(ctx, c.Param("id"), input)
	if err != nil {
		serviceError(c, err)
		return
	}
	a.withStatus(&item)
	respond(c, http.StatusOK, item)
}

func (a *api) remove(c *gin.Context) {
	if err := a.Instances.Delete(c.Request.Context(), c.Param("id"), c.Query("history")); err != nil {
		serviceError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"deleted": true, "history": c.Query("history")})
}
