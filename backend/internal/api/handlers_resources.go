package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/l-lab/cloud-agents/internal/auth"
	"github.com/l-lab/cloud-agents/internal/db"
	"github.com/l-lab/cloud-agents/pkg/logger"
)

var validResourceName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

const (
	maxSkillFiles    = 20
	maxSkillFileSize = 1 << 20 // 1 MiB
)

var validSkillFilePath = regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`)

func isValidSkillFilePath(p string) bool {
	if p == "" || !validSkillFilePath.MatchString(p) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return false
		}
	}
	return true
}

// ResourceHandler serves the resources REST API.
type ResourceHandler struct {
	kindsRepo db.KindsRepository
	ofsWriter ResourceWriter
	ofsReader ResourceReader
}

// NewResourceHandler constructs a ResourceHandler from its dependencies.
func NewResourceHandler(repo db.KindsRepository, w ResourceWriter, r ResourceReader) *ResourceHandler {
	return &ResourceHandler{
		kindsRepo: repo,
		ofsWriter: w,
		ofsReader: r,
	}
}

func kindRecordToResponse(r *db.KindRecord) resourceResponse {
	return resourceResponse{
		ID:        r.ID,
		Kind:      r.Kind,
		Name:      r.Name,
		OFSPath:   r.OFSPath,
		Meta:      r.Meta,
		IsActive:  r.IsActive,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
}

// resource-domain request/response types

type createResourceRequest struct {
	Kind    string          `json:"kind" binding:"required"`
	Name    string          `json:"name" binding:"required"`
	Content string          `json:"content"`
	Meta    json.RawMessage `json:"meta,omitempty"`
}

type updateResourceRequest struct {
	Content  string          `json:"content,omitempty"`
	Meta     json.RawMessage `json:"meta,omitempty"`
	IsActive *bool           `json:"is_active,omitempty"`
}

type resourceResponse struct {
	ID        int             `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	OFSPath   string          `json:"ofs_path"`
	Meta      json.RawMessage `json:"meta"`
	IsActive  bool            `json:"is_active"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// CreateResource handles POST /api/resources.
//
// @Summary      Create a resource
// @Description  Create a skill (SKILL.md) or mcp (JSON config) resource owned by the authenticated user.
// @Tags         resources
// @Accept       json
// @Produce      json
// @Param        body  body      createResourceRequest  true  "Resource definition"
// @Success      201   {object}  resourceResponse
// @Failure      400   {object}  errorResponse  "invalid request body"
// @Failure      401   {object}  errorResponse  "unauthorized"
// @Failure      500   {object}  errorResponse  "failed to store resource"
// @Failure      503   {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources [post]
func (h *ResourceHandler) CreateResource(c *gin.Context) {
	if h.kindsRepo == nil || h.ofsWriter == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var body createResourceRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if body.Kind != "skill" && body.Kind != "mcp" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind must be 'skill' or 'mcp'"})
		return
	}
	if !validResourceName.MatchString(body.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must match ^[a-zA-Z0-9_-]+$"})
		return
	}

	var ofsPath string
	var meta json.RawMessage
	var ofsKey string
	var ofsContent []byte

	switch body.Kind {
	case "skill":
		if body.Content == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "content (SKILL.md) is required for skill resources"})
			return
		}
		ofsPath = fmt.Sprintf("%s/resources/skills/%s/", u.UserName, body.Name)
		ofsKey = ofsPath + "SKILL.md"
		ofsContent = []byte(body.Content)
		initMeta, _ := json.Marshal(db.SkillMeta{Files: []string{"SKILL.md"}})
		meta = initMeta
	case "mcp":
		ofsPath = fmt.Sprintf("%s/resources/mcp/%s.json", u.UserName, body.Name)
		ofsKey = ofsPath
		raw := body.Meta
		if len(raw) == 0 {
			raw = json.RawMessage(body.Content)
		}
		if !json.Valid(raw) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "mcp meta must be valid JSON"})
			return
		}
		meta = raw
		ofsContent = []byte(meta)
	}

	if err := h.ofsWriter.PutObject(c.Request.Context(), ofsKey, ofsContent); err != nil {
		logger.Default().Error("put resource to OFS", "key", ofsKey, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store resource"})
		return
	}

	rec, err := h.kindsRepo.Create(c.Request.Context(), u.ID, body.Kind, body.Name, ofsPath, meta)
	if err != nil {
		logger.Default().Error("create kind record", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create resource"})
		return
	}

	c.JSON(http.StatusCreated, kindRecordToResponse(rec))
}

// CreateSkillFromZip handles POST /api/resources/zip.
//
// @Summary      Create a skill from a zip archive
// @Description  Create a multi-file skill by uploading a zip. The zip must contain SKILL.md at root. Other files are stored as companion files (max 20 total, 1 MiB each).
// @Tags         resources
// @Accept       multipart/form-data
// @Produce      json
// @Param        name  formData  string  true  "Skill name ([a-zA-Z0-9_-]+)"
// @Param        file  formData  file    true  "Zip archive"
// @Success      201   {object}  resourceResponse
// @Failure      400   {object}  errorResponse  "invalid name, missing SKILL.md, or bad zip"
// @Failure      401   {object}  errorResponse  "unauthorized"
// @Failure      413   {object}  errorResponse  "zip or file too large"
// @Failure      422   {object}  errorResponse  "too many files"
// @Failure      500   {object}  errorResponse  "storage error"
// @Failure      503   {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources/zip [post]
func (h *ResourceHandler) CreateSkillFromZip(c *gin.Context) {
	if h.kindsRepo == nil || h.ofsWriter == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	name := c.PostForm("name")
	if !validResourceName.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must match ^[a-zA-Z0-9_-]+$"})
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}

	const maxZipSize = maxSkillFiles * maxSkillFileSize
	f, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read upload"})
		return
	}
	defer f.Close()

	zipBytes, err := io.ReadAll(io.LimitReader(f, maxZipSize+1))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read upload"})
		return
	}
	if len(zipBytes) > maxZipSize {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("zip exceeds %d MiB total limit", maxZipSize>>20)})
		return
	}

	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid zip file"})
		return
	}

	type zipEntry struct {
		path    string
		content []byte
	}
	var rawEntries []zipEntry
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		if strings.HasPrefix(zf.Name, "__MACOSX/") {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read zip entry"})
			return
		}
		content, readErr := io.ReadAll(io.LimitReader(rc, maxSkillFileSize+1))
		rc.Close()
		if readErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read zip entry"})
			return
		}
		if len(content) > maxSkillFileSize {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("file %q exceeds 1 MiB limit", zf.Name)})
			return
		}
		rawEntries = append(rawEntries, zipEntry{path: zf.Name, content: content})
	}

	if len(rawEntries) > 0 {
		prefix := ""
		if i := strings.IndexByte(rawEntries[0].path, '/'); i >= 0 {
			prefix = rawEntries[0].path[:i]
		}
		if prefix != "" {
			allMatch := true
			for _, e := range rawEntries[1:] {
				if !strings.HasPrefix(e.path, prefix+"/") {
					allMatch = false
					break
				}
			}
			if allMatch {
				for i := range rawEntries {
					rawEntries[i].path = strings.TrimPrefix(rawEntries[i].path, prefix+"/")
				}
			}
		}
	}

	var companions []zipEntry
	var skillMDContent []byte
	for _, e := range rawEntries {
		if !isValidSkillFilePath(e.path) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid file path in zip: %q", e.path)})
			return
		}
		if e.path == "SKILL.md" {
			skillMDContent = e.content
		} else {
			companions = append(companions, e)
		}
	}

	if skillMDContent == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "zip must contain SKILL.md at root"})
		return
	}
	if 1+len(companions) > maxSkillFiles {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": fmt.Sprintf("skill cannot exceed %d files", maxSkillFiles)})
		return
	}

	ofsPath := fmt.Sprintf("%s/resources/skills/%s/", u.UserName, name)

	if err := h.ofsWriter.PutObject(c.Request.Context(), ofsPath+"SKILL.md", skillMDContent); err != nil {
		logger.Default().Error("put SKILL.md to OFS", "key", ofsPath+"SKILL.md", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store resource"})
		return
	}

	initMeta, _ := json.Marshal(db.SkillMeta{Files: []string{"SKILL.md"}})
	rec, err := h.kindsRepo.Create(c.Request.Context(), u.ID, "skill", name, ofsPath, initMeta)
	if err != nil {
		logger.Default().Error("create kind record for zip skill", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create resource"})
		return
	}

	if len(companions) > 0 {
		files := []string{"SKILL.md"}
		for _, entry := range companions {
			ofsKey := ofsPath + entry.path
			if err := h.ofsWriter.PutObject(c.Request.Context(), ofsKey, entry.content); err != nil {
				logger.Default().Error("put companion file to OFS", "key", ofsKey, "err", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to store file %q", entry.path)})
				return
			}
			files = append(files, entry.path)
		}
		newMeta, _ := json.Marshal(db.SkillMeta{Files: files})
		rec, err = h.kindsRepo.Update(c.Request.Context(), rec.ID, u.ID, db.KindUpdate{Meta: newMeta})
		if err != nil {
			logger.Default().Error("update skill meta after zip upload", "id", rec.ID, "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update resource"})
			return
		}
	}

	c.JSON(http.StatusCreated, kindRecordToResponse(rec))
}

// ListResources handles GET /api/resources.
//
// @Summary      List resources
// @Description  List all resources owned by the authenticated user.
// @Tags         resources
// @Produce      json
// @Success      200  {array}   resourceResponse
// @Failure      401  {object}  errorResponse  "unauthorized"
// @Failure      500  {object}  errorResponse  "failed to list resources"
// @Failure      503  {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources [get]
func (h *ResourceHandler) ListResources(c *gin.Context) {
	if h.kindsRepo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	records, err := h.kindsRepo.List(c.Request.Context(), u.ID)
	if err != nil {
		logger.Default().Error("list resources", "user_id", u.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list resources"})
		return
	}

	items := make([]resourceResponse, len(records))
	for i, r := range records {
		items[i] = kindRecordToResponse(r)
	}
	c.JSON(http.StatusOK, items)
}

// UpdateResource handles PUT /api/resources/:id.
//
// @Summary      Update a resource
// @Description  Update a resource's content, meta, or active flag.
// @Tags         resources
// @Accept       json
// @Produce      json
// @Param        id    path      int                    true  "Resource ID"
// @Param        body  body      updateResourceRequest  true  "Update fields"
// @Success      200   {object}  resourceResponse
// @Failure      400   {object}  errorResponse  "invalid request body"
// @Failure      401   {object}  errorResponse  "unauthorized"
// @Failure      404   {object}  errorResponse  "resource not found"
// @Failure      500   {object}  errorResponse  "failed to update resource"
// @Failure      503   {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources/{id} [put]
func (h *ResourceHandler) UpdateResource(c *gin.Context) {
	if h.kindsRepo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var body updateResourceRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	update := db.KindUpdate{IsActive: body.IsActive}

	if body.Content != "" {
		if h.ofsWriter == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
			return
		}
		rec, err := h.kindsRepo.Get(c.Request.Context(), id, u.ID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
			return
		}

		var ofsKey string
		var ofsContent []byte
		switch rec.Kind {
		case "skill":
			ofsKey = rec.OFSPath + "SKILL.md"
			ofsContent = []byte(body.Content)
		case "mcp":
			if !json.Valid([]byte(body.Content)) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "content must be valid JSON for mcp kind"})
				return
			}
			ofsKey = rec.OFSPath
			ofsContent = []byte(body.Content)
			update.Meta = json.RawMessage(body.Content)
		}

		if err := h.ofsWriter.PutObject(c.Request.Context(), ofsKey, ofsContent); err != nil {
			logger.Default().Error("update resource in OFS", "key", ofsKey, "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update resource"})
			return
		}
	}

	if len(body.Meta) > 0 && update.Meta == nil {
		update.Meta = body.Meta
	}

	result, err := h.kindsRepo.Update(c.Request.Context(), id, u.ID, update)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}

	c.JSON(http.StatusOK, kindRecordToResponse(result))
}

// DeleteResource handles DELETE /api/resources/:id.
//
// @Summary      Delete a resource
// @Description  Delete a resource owned by the authenticated user. OFS content is not removed.
// @Tags         resources
// @Param        id   path  int  true  "Resource ID"
// @Success      204
// @Failure      400  {object}  errorResponse  "invalid id"
// @Failure      401  {object}  errorResponse  "unauthorized"
// @Failure      404  {object}  errorResponse  "resource not found"
// @Failure      503  {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources/{id} [delete]
func (h *ResourceHandler) DeleteResource(c *gin.Context) {
	if h.kindsRepo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	if err := h.kindsRepo.Delete(c.Request.Context(), id, u.ID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}

	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()
}

// GetSkillContent handles GET /api/resources/:id/content.
//
// @Summary      Get skill SKILL.md content
// @Description  Reads and returns the SKILL.md content for a skill resource from OFS.
// @Tags         resources
// @Produce      text/plain
// @Param        id   path      int  true  "Resource ID"
// @Success      200  {string}  string  "SKILL.md content"
// @Failure      400  {object}  errorResponse  "resource is not a skill"
// @Failure      401  {object}  errorResponse  "unauthorized"
// @Failure      404  {object}  errorResponse  "resource not found"
// @Failure      503  {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources/{id}/content [get]
func (h *ResourceHandler) GetSkillContent(c *gin.Context) {
	if h.kindsRepo == nil || h.ofsReader == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	rec, err := h.kindsRepo.Get(c.Request.Context(), id, u.ID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}
	if rec.Kind != "skill" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content endpoint is only available for skill resources"})
		return
	}

	ofsKey := rec.OFSPath + "SKILL.md"
	data, err := h.ofsReader.GetObjectBytes(c.Request.Context(), ofsKey)
	if err != nil {
		logger.Default().Error("read SKILL.md from OFS", "key", ofsKey, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read skill content"})
		return
	}

	c.Data(http.StatusOK, "text/plain; charset=utf-8", data)
}

// UpsertSkillFile handles PUT /api/resources/:id/files/*filepath.
//
// @Summary      Upload or overwrite a skill file
// @Description  Upload (or overwrite) a single file inside a skill resource. Body is the raw file content (max 1 MiB). Skills are capped at 20 files.
// @Tags         resources
// @Accept       octet-stream
// @Produce      json
// @Param        id        path      int     true  "Resource ID"
// @Param        filepath  path      string  true  "Relative file path inside the skill"
// @Param        body      body      string  true  "Raw file bytes"
// @Success      200       {object}  resourceResponse
// @Failure      400       {object}  errorResponse  "invalid id or file path"
// @Failure      401       {object}  errorResponse  "unauthorized"
// @Failure      404       {object}  errorResponse  "resource not found"
// @Failure      413       {object}  errorResponse  "file exceeds size limit"
// @Failure      422       {object}  errorResponse  "skill file count exceeds limit"
// @Failure      500       {object}  errorResponse  "failed to store file"
// @Failure      503       {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources/{id}/files/{filepath} [put]
func (h *ResourceHandler) UpsertSkillFile(c *gin.Context) {
	if h.kindsRepo == nil || h.ofsWriter == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	filePath := strings.TrimPrefix(c.Param("filepath"), "/")
	if !isValidSkillFilePath(filePath) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file path: must match [a-zA-Z0-9_./-]+ with no empty, '.', or '..' segments"})
		return
	}

	content, err := io.ReadAll(io.LimitReader(c.Request.Body, maxSkillFileSize+1))
	if err != nil {
		logger.Default().Error("read skill file body", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read request body"})
		return
	}
	if len(content) > maxSkillFileSize {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("file exceeds %d MiB limit", maxSkillFileSize>>20)})
		return
	}

	rec, err := h.kindsRepo.Get(c.Request.Context(), id, u.ID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}
	if rec.Kind != "skill" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file management is only supported for skill resources"})
		return
	}

	files := rec.SkillFiles()
	isNew := true
	for _, f := range files {
		if f == filePath {
			isNew = false
			break
		}
	}
	if isNew && len(files) >= maxSkillFiles {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": fmt.Sprintf("skill cannot exceed %d files", maxSkillFiles)})
		return
	}

	ofsKey := rec.OFSPath + filePath
	if err := h.ofsWriter.PutObject(c.Request.Context(), ofsKey, content); err != nil {
		logger.Default().Error("put skill file to OFS", "key", ofsKey, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store file"})
		return
	}

	var result *db.KindRecord
	if isNew {
		newMeta, _ := json.Marshal(db.SkillMeta{Files: append(files, filePath)})
		result, err = h.kindsRepo.Update(c.Request.Context(), id, u.ID, db.KindUpdate{Meta: newMeta})
		if err != nil {
			logger.Default().Error("update skill meta", "id", id, "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update resource"})
			return
		}
	} else {
		result = rec
	}

	c.JSON(http.StatusOK, kindRecordToResponse(result))
}

// DeleteSkillFile handles DELETE /api/resources/:id/files/*filepath.
//
// @Summary      Remove a file from a skill
// @Description  Remove a file from a skill's manifest. SKILL.md cannot be removed; OFS content is not deleted.
// @Tags         resources
// @Produce      json
// @Param        id        path      int     true  "Resource ID"
// @Param        filepath  path      string  true  "Relative file path inside the skill"
// @Success      200       {object}  resourceResponse
// @Failure      400       {object}  errorResponse  "invalid id, invalid path, or SKILL.md cannot be removed"
// @Failure      401       {object}  errorResponse  "unauthorized"
// @Failure      404       {object}  errorResponse  "resource or file not found"
// @Failure      500       {object}  errorResponse  "failed to update resource"
// @Failure      503       {object}  errorResponse  "resource storage not configured"
// @Router       /api/resources/{id}/files/{filepath} [delete]
func (h *ResourceHandler) DeleteSkillFile(c *gin.Context) {
	if h.kindsRepo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "resource storage not configured"})
		return
	}
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	filePath := strings.TrimPrefix(c.Param("filepath"), "/")
	if filePath == "SKILL.md" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SKILL.md cannot be removed"})
		return
	}
	if !isValidSkillFilePath(filePath) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file path"})
		return
	}

	rec, err := h.kindsRepo.Get(c.Request.Context(), id, u.ID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}
	if rec.Kind != "skill" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file management is only supported for skill resources"})
		return
	}

	files := rec.SkillFiles()
	newFiles := make([]string, 0, len(files))
	found := false
	for _, f := range files {
		if f == filePath {
			found = true
			continue
		}
		newFiles = append(newFiles, f)
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found in skill"})
		return
	}

	newMeta, _ := json.Marshal(db.SkillMeta{Files: newFiles})
	result, err := h.kindsRepo.Update(c.Request.Context(), id, u.ID, db.KindUpdate{Meta: newMeta})
	if err != nil {
		logger.Default().Error("remove skill file from meta", "id", id, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update resource"})
		return
	}

	c.JSON(http.StatusOK, kindRecordToResponse(result))
}
