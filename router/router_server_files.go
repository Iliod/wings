package router

import (
	"bufio"
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/pterodactyl/wings/server"
	"golang.org/x/sync/errgroup"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
)

// Returns the contents of a file on the server.
func getServerFileContents(c *gin.Context) {
	s := GetServer(c.Param("server"))

	p, err := url.QueryUnescape(c.Query("file"))
	if err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}
	p = "/" + strings.TrimLeft(p, "/")

	cleaned, err := s.Filesystem.SafePath(p)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
			"error": "The file requested could not be found.",
		})
		return
	}

	st, err := s.Filesystem.Stat(cleaned)
	if err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}

	if st.Info.IsDir() {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
			"error": "The requested resource was not found on the system.",
		})
		return
	}

	f, err := os.Open(cleaned)
	if err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}
	defer f.Close()

	c.Header("X-Mime-Type", st.Mimetype)
	c.Header("Content-Length", strconv.Itoa(int(st.Info.Size())))

	// If a download parameter is included in the URL go ahead and attach the necessary headers
	// so that the file can be downloaded.
	if c.Query("download") != "" {
		c.Header("Content-Disposition", "attachment; filename="+st.Info.Name())
		c.Header("Content-Type", "application/octet-stream")
	}

	bufio.NewReader(f).WriteTo(c.Writer)
}

// Returns the contents of a directory for a server.
func getServerListDirectory(c *gin.Context) {
	s := GetServer(c.Param("server"))

	d, err := url.QueryUnescape(c.Query("directory"))
	if err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}

	stats, err := s.Filesystem.ListDirectory(d)
	if err != nil {
		if err.Error() == "readdirent: not a directory" {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"error": "The requested directory does not exist.",
			})
			return
		}

		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}

	c.JSON(http.StatusOK, stats)
}

type renameFile struct {
	To   string `json:"to"`
	From string `json:"from"`
}

// Renames (or moves) files for a server.
func putServerRenameFiles(c *gin.Context) {
	s := GetServer(c.Param("server"))

	var data struct {
		Root  string       `json:"root"`
		Files []renameFile `json:"files"`
	}
	// BindJSON sends 400 if the request fails, all we need to do is return
	if err := c.BindJSON(&data); err != nil {
		return
	}

	if len(data.Files) == 0 {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
			"error": "No files to move or rename were provided.",
		})
		return
	}

	g, ctx := errgroup.WithContext(context.Background())

	// Loop over the array of files passed in and perform the move or rename action against each.
	for _, p := range data.Files {
		pf := path.Join(data.Root, p.From)
		pt := path.Join(data.Root, p.To)

		g.Go(func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				if err := s.Filesystem.Rename(pf, pt); err != nil {
					// Return nil if the error is an is not exists.
					// NOTE: os.IsNotExist() does not work if the error is wrapped.
					if errors.Is(err, os.ErrNotExist) {
						return nil
					}

					return err
				}

				return nil
			}
		})
	}

	if err := g.Wait(); err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}

	c.Status(http.StatusNoContent)
}

// Copies a server file.
func postServerCopyFile(c *gin.Context) {
	s := GetServer(c.Param("server"))

	var data struct {
		Location string `json:"location"`
	}
	// BindJSON sends 400 if the request fails, all we need to do is return
	if err := c.BindJSON(&data); err != nil {
		return
	}

	if err := s.Filesystem.Copy(data.Location); err != nil {
		// Check if the file does not exist.
		// NOTE: os.IsNotExist() does not work if the error is wrapped.
		if errors.Is(err, os.ErrNotExist) {
			c.Status(http.StatusNotFound)
			return
		}

		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}

	c.Status(http.StatusNoContent)
}

// Deletes files from a server.
func postServerDeleteFiles(c *gin.Context) {
	s := GetServer(c.Param("server"))

	var data struct {
		Root  string   `json:"root"`
		Files []string `json:"files"`
	}

	if err := c.BindJSON(&data); err != nil {
		return
	}

	if len(data.Files) == 0 {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
			"error": "No files were specified for deletion.",
		})
		return
	}

	g, ctx := errgroup.WithContext(context.Background())

	// Loop over the array of files passed in and delete them. If any of the file deletions
	// fail just abort the process entirely.
	for _, p := range data.Files {
		pi := path.Join(data.Root, p)

		g.Go(func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return s.Filesystem.Delete(pi)
			}
		})
	}

	if err := g.Wait(); err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}

	c.Status(http.StatusNoContent)
}

// Writes the contents of the request to a file on a server.
func postServerWriteFile(c *gin.Context) {
	s := GetServer(c.Param("server"))

	f, err := url.QueryUnescape(c.Query("file"))
	if err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}
	f = "/" + strings.TrimLeft(f, "/")

	if err := s.Filesystem.Writefile(f, c.Request.Body); err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}

	c.Status(http.StatusNoContent)
}

// Create a directory on a server.
func postServerCreateDirectory(c *gin.Context) {
	s := GetServer(c.Param("server"))

	var data struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	// BindJSON sends 400 if the request fails, all we need to do is return
	if err := c.BindJSON(&data); err != nil {
		return
	}

	if err := s.Filesystem.CreateDirectory(data.Name, data.Path); err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}

	c.Status(http.StatusNoContent)
}

func postServerCompressFiles(c *gin.Context) {
	s := GetServer(c.Param("server"))

	var data struct {
		RootPath string   `json:"root"`
		Files    []string `json:"files"`
	}

	if err := c.BindJSON(&data); err != nil {
		return
	}

	if len(data.Files) == 0 {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
			"error": "No files were passed through to be compressed.",
		})
		return
	}

	if !s.Filesystem.HasSpaceAvailable() {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{
			"error": "This server does not have enough available disk space to generate a compressed archive.",
		})
		return
	}

	f, err := s.Filesystem.CompressFiles(data.RootPath, data.Files)
	if err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}

	c.JSON(http.StatusOK, &server.Stat{
		Info:     f,
		Mimetype: "application/tar+gzip",
	})
}

func postServerDecompressFiles(c *gin.Context) {
	s := GetServer(c.Param("server"))

	var data struct {
		RootPath string `json:"root"`
		File     string `json:"file"`
	}

	if err := c.BindJSON(&data); err != nil {
		return
	}

	hasSpace, err := s.Filesystem.SpaceAvailableForDecompression(data.RootPath, data.File)
	if err != nil {
		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}

	if !hasSpace {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{
			"error": "This server does not have enough available disk space to decompress this archive.",
		})
		return
	}

	if err := s.Filesystem.DecompressFile(data.RootPath, data.File); err != nil {
		// Check if the file does not exist.
		// NOTE: os.IsNotExist() does not work if the error is wrapped.
		if errors.Is(err, os.ErrNotExist) {
			c.Status(http.StatusNotFound)
			return
		}

		TrackedServerError(err, s).AbortWithServerError(c)
		return
	}

	c.Status(http.StatusNoContent)
}
