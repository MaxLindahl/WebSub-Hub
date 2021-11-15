package main

//todo: error handling, edge cases etc

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/Joker666/AsyncGoDemo/async"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/orcaman/concurrent-map"
	"io"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type subscription struct {
	callback         string
	secret           string
	lease            int
	subscriptionDate time.Time
}

func main() {
	e := echo.New()
	e.Use(middleware.Logger())                             // Logger
	e.Use(middleware.Recover())                            // Recover
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{ //add CORS
		AllowOrigins: []string{"*"},
		AllowMethods: []string{echo.GET, echo.HEAD, echo.PUT, echo.PATCH, echo.POST, echo.DELETE},
	}))
	e.POST("/", subscribe)
	e.Any("/publish", Publish)

	e.Logger.Fatal(e.Start(":8080"))
}

/////////////global variables/////////////////
var client = http.Client{
	Timeout: 5 * time.Second,
}
var subscribers = cmap.New() //concurrent map
//////////////////////////////////////////////
func subscribe(c echo.Context) error {

	//get form values and assign to variables for easy use
	callback := c.FormValue("hub.callback")
	secret := []byte(c.FormValue("hub.secret"))
	mode := []byte(c.FormValue("hub.mode"))
	topic := []byte(c.FormValue("hub.topic"))

	async.Exec(func() interface{} { //async call to continue the subscription process so that we can return to the subscriber that we are working on it
		AttemptRegistration(callback, secret, topic, mode)
		return 1
	})

	return c.String(http.StatusOK, "Request accepted")
	//is there a situation i want to decline a request? (read 5.2 how to decline)

}

func AttemptRegistration(callback string, secret []byte, topic []byte, mode []byte) {

	//create a random string for verification of intent - hub.challenge value
	challenge := RandStringBytes(10)
	lease := 84600 //lease time, temp value 86400s = 1 day

	log.Println("mode is : " + string(mode))
	log.Println("secret is: " + hex.EncodeToString(secret))
	log.Println("callback is : " + callback)

	u, r := url.Parse(callback)
	if r != nil {
		log.Println("error parsing url")
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		log.Println("error parsing query")
	}
	q.Add("hub.mode", string(mode))
	q.Add("hub.topic", string(topic))
	q.Add("hub.challenge", challenge)
	q.Add("hub.lease_seconds", strconv.Itoa(lease))
	u.RawQuery = q.Encode()
	//create a request for verification
	req, erro := http.NewRequest("GET", u.String(), nil)
	if erro != nil {
		//handle it
		log.Println("new request error Madge!")
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept-Charset", "utf-8")

	err := req.ParseForm()
	if err != nil {
		return
	}

	//send the request
	resp, er := client.Do(req)
	log.Println("Verification request sent!")
	if er != nil {
		log.Println("req FAILED!")
		log.Println(er.Error())
		return
	}
	log.Println(resp.StatusCode)
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(resp.Body)
	b, _ := ioutil.ReadAll(resp.Body)
	log.Println("Response body: " + string(b))
	if string(b) != challenge { //check if returned body matches challenge
		log.Println("Error: Returned challenge does not match")
		return
	}

	if resp.StatusCode < 300 && resp.StatusCode >= 200 {
		if challenge == string(b) {
			log.Println("Returned challenge is correct, continuing")

			if string(mode) == "unsubscribe" { //if the user wants to unsub
				log.Println("unsubbing")
				subscribers.Remove(callback)
				return
			} else { //user wants to sub
				log.Println("subbing")
				subscribers.Set(callback, subscription{
					callback:         callback,
					secret:           string(secret),
					lease:            lease,
					subscriptionDate: time.Now(),
				})
				return
			}
		} else {
			log.Println("Returned challenge is wrong, action stopped")
			return
		}

	} else {
		log.Println("FAILURE! we could not subscribe!")
		return
	}
}

const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func RandStringBytes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[rand.Intn(len(letterBytes))]
	}
	return string(b)
}

func Sign(msg, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)

	return hex.EncodeToString(mac.Sum(nil))
}

func Publish(c echo.Context) error {
	log.Println("Got to publish!")
	log.Println("len subs: " + strconv.Itoa(subscribers.Count()))
	subsToRemove := make([]string, 0, subscribers.Count())
	log.Println("remove array made")
	randData := []byte(`{"data":"THISISCOOLDATA"}`)
	log.Println("Random data assigned")
	for tuple := range subscribers.IterBuffered() {
		key := tuple.Key
		value := tuple.Val.(subscription)
		if getSecondsSinceSubscribed(value.subscriptionDate) > value.lease {
			log.Println("Adding sub to remove list")
			subsToRemove = append(subsToRemove, key) //if enough time has passed so that the subscription has expired, add the key to a list of subs to remove and don't send json
		} else {
			log.Println("posting data to sub!")
			PostJsonToSub(randData, value) //if within lease time, send json
		}
	}
	log.Println("got out of first loop!")
	for i := 0; i < len(subsToRemove); i++ { //loop through all keys found to have expired lease time and remove them from the map
		log.Println("removing a sub")
		subscribers.Remove(subsToRemove[i])
	}
	log.Println("publish: the end")
	return c.String(http.StatusOK, "Published data to subscribers")
}

func PostJsonToSub(data []byte, sub subscription) {
	req, err := http.NewRequest("POST", sub.callback, bytes.NewBuffer(data))
	if err != nil {
		log.Println("Error creating post request for sub: " + sub.callback + " error: " + err.Error())
		return
	}
	hash := Sign(data, []byte(sub.secret))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature", "sha256="+hash)
	resp, erro := client.Do(req)
	if err != nil {
		log.Println("Error sending data to: " + sub.callback + " error: " + erro.Error())
		return
	}
	if resp.StatusCode < 300 && resp.StatusCode >= 200 {
		log.Println("Data successfully sent!")
	} else {
		log.Println("Error sending data: " + strconv.Itoa(resp.StatusCode))
	}
	return
}

//help function to get time passed in seconds since given date
func getSecondsSinceSubscribed(subscribeDate time.Time) int {
	currentTime := time.Now()
	log.Println("current time: " + currentTime.String())
	log.Println("Sub time: " + subscribeDate.String())
	return int(math.Abs(subscribeDate.Sub(currentTime).Seconds()))
}
