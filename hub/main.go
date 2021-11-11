package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/labstack/echo/v4"
	"log"
	"net/http"
)

func main() {
	e := echo.New()
	e.POST("/", subscribe)
	e.Logger.Fatal(e.Start(":8080"))
}

func subscribe(c echo.Context) error {

	c.Logger().Print("WE FOUND THE HUB!")
	//is this correct? prob not tbh
	callback := c.QueryParams().Get("callback")
	secret := []byte(c.QueryParams().Get("secret"))
	mode := []byte(c.QueryParams().Get("mode"))
	topic := []byte(c.QueryParams().Get("topic"))
	hash := Sign(mode, secret)

	signature := "X-Hub-Signature: sha256=" + hash

	client := http.Client{}
	req, err := http.NewRequest("GET", callback, nil)
	if err!= nil {
		//handle it
	}
	req.Header = http.Header{
		//add headers
	}
	q := req.URL.Query()
	q.Add("mode", string(mode))
	q.Add("topic", string(topic))
	q.Add("challenge", signature)
	q.Add("lease_seconds", "3")

	req.URL.RawQuery = q.Encode()
	resp, erro := client.Do(req)
	if erro != nil {
		log.Fatalln(err)
	}
	if resp.StatusCode <300 && resp.StatusCode >=200 {
		return c.String(http.StatusOK, "Subscribed!")
	}else {
		return c.String(http.StatusNotAcceptable, "Something went wrong :(")
	}


}

func Sign(msg, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)

	return hex.EncodeToString(mac.Sum(nil))
}

