package main

//todo: fix get request to subscriber (it complains there is no hub.mode)
//todo: implement lease time thingy
//todo: Optimize locks
//todo: error handling, edge cases etc

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/Joker666/AsyncGoDemo/async"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type subscription struct {
	callback         string
	secret           string
	lease            int
	subscriptionDate string
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
var client = http.Client{}
var subscribers = make(map[string]subscription) //hold current subscribers key is same as callback
var mutex = &sync.RWMutex{}                     //lock for map
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
	lease := 86400 //lease time, temp value 86400s = 1 day

	log.Println("mode is : " + string(mode))
	log.Println("secret is: " + hex.EncodeToString(secret))
	log.Println("callback is : " + callback)

	//Create the body for the verification request
	form := make(url.Values)

	//create a request for verification
	req, erro := http.NewRequest("GET", callback, strings.NewReader(form.Encode()))
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

	req.Form.Set("hub.mode", string(mode))
	req.Form.Set("hub.topic", string(topic))
	req.Form.Set("hub.challenge", challenge)
	req.Form.Set("hub.lease_seconds", strconv.Itoa(lease))
	req.Header.Set("Content-Length", strconv.Itoa(len(req.Form.Encode())))

	log.Println("Mode to be sent is: " + req.FormValue("hub.mode"))
	log.Println("topic to be sent is: " + req.FormValue("hub.topic"))
	log.Println("challenge to be sent is: " + req.FormValue("hub.challenge"))
	log.Println("lease to be sent is: " + req.FormValue("hub.lease_seconds"))

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
			mutex.RLock()                      //lock read access
			mutex.Lock()                       //lock write access
			if string(mode) == "unsubscribe" { //if the user wants to unsub
				delete(subscribers, callback)
				mutex.Unlock() //release locks
				mutex.RUnlock()
				return
			} else { //user wants to sub
				subscribers[callback] = subscription{
					callback:         callback,
					secret:           string(secret),
					lease:            lease,
					subscriptionDate: time.Now().String(),
				}
				mutex.Unlock() //release locks
				mutex.RUnlock()
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
	subsToRemove := make([]string, 0, len(subscribers))
	randData := []byte(`{"data":"THISISCOOLDATA"}`)
	mutex.Lock() //lock read and write access while iterating through map
	mutex.RLock()
	for key, value := range subscribers { // Order not specified
		if getSecondsSinceSubscribed(value.subscriptionDate) > value.lease {
			subsToRemove = append(subsToRemove, key) //if enough time has passed so that the subscription has expired, add the key to a list of subs to remove and don't send json
		} else {
			PostJsonToSub(randData, value) //if within lease time, send json
		}
	}
	for i := 0; i < len(subsToRemove); i++ { //loop through all keys found to have expired lease time and remove them from the map
		delete(subscribers, subsToRemove[i])
	}
	mutex.Unlock()
	mutex.RUnlock()

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
func getSecondsSinceSubscribed(subscribeDateStr string) int {
	currentTime := time.Now()
	loc := currentTime.Location()
	layout := "2006-01-02 15:04"
	subscribeDate, err := time.ParseInLocation(layout, subscribeDateStr, loc)
	if err != nil {
		log.Println(err)
	}
	return int(subscribeDate.Sub(currentTime).Seconds())
}
