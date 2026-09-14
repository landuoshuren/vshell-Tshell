package agent

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func uploadFileToOSS(p map[string]any) error {
	endpoint := str(p, "Endpoint")
	accessKeyID := str(p, "AccessKeyId")
	accessKeySecret := str(p, "AccessKeySecret")
	bucket := str(p, "BucketName")
	if endpoint == "" || accessKeyID == "" || accessKeySecret == "" || bucket == "" {
		return errors.New("incomplete OSS configuration")
	}
	localPath := joinTarget(p)
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	if endpointURL.Scheme == "" {
		endpointURL, err = url.Parse("https://" + endpoint)
		if err != nil {
			return err
		}
	}
	objectName := strings.ReplaceAll(strings.TrimRight(str(p, "path"), "/\\")+"/"+str(p, "target"), "\\", "/")
	resource := "/" + bucket + "/" + objectName
	hostname := endpointURL.Hostname()
	if net.ParseIP(hostname) != nil || strings.EqualFold(hostname, "localhost") {
		endpointURL.Path = strings.TrimRight(endpointURL.Path, "/") + resource
	} else {
		port := endpointURL.Port()
		endpointURL.Host = bucket + "." + hostname
		if port != "" {
			endpointURL.Host += ":" + port
		}
		endpointURL.Path = strings.TrimRight(endpointURL.Path, "/") + "/" + objectName
	}
	contentType := ossContentType(filepath.Ext(localPath))
	date := time.Now().UTC().Format(http.TimeFormat)
	stringToSign := "PUT\n\n" + contentType + "\n" + date + "\n" + resource
	mac := hmac.New(sha1.New, []byte(accessKeySecret))
	_, _ = io.WriteString(mac, stringToSign)
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	request, err := http.NewRequest(http.MethodPut, endpointURL.String(), file)
	if err != nil {
		return err
	}
	request.ContentLength = info.Size()
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Date", date)
	request.Header.Set("Authorization", "OSS "+accessKeyID+":"+signature)
	client := &http.Client{Timeout: 30 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("OSS status %s", response.Status)
	}
	return nil
}

func ossContentType(extension string) string {
	switch strings.ToLower(extension) {
	case ".txt", ".log", ".csv":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	default:
		return "application/octet-stream"
	}
}
