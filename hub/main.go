package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/Joker666/AsyncGoDemo/async"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/orcaman/concurrent-map"
	"github.com/tomnomnom/linkheader"
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

//struct to hold a subscription
type subscription struct {
	callback         string
	secret           string
	topic            string
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

	//set up routing
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

// subscribe called from POST request to hub address
//initiates a subscription or unsubscription attempt
func subscribe(c echo.Context) error {

	//get form values and assign to variables for use
	callback := c.FormValue("hub.callback")
	secret := []byte(c.FormValue("hub.secret"))
	mode := []byte(c.FormValue("hub.mode"))
	topic := []byte(c.FormValue("hub.topic"))

	//check if the request is valid by making sure the input is in correct format
	if isInputCorrect(callback, string(secret), string(topic), string(mode)) {
		//input is correct
		async.Exec(func() interface{} { //async call to continue the subscription process so that we can return to the subscriber that we are working on it
			AttemptRegistration(callback, secret, topic, mode)
			return 1
		})

		return c.String(http.StatusOK, "Request accepted")
	} else {
		//input is not correct
		async.Exec(func() interface{} { //async call to inform the subscriber why it failed
			InformSubscribeFailed(callback, string(topic))
			return 1
		})

		return c.String(http.StatusBadRequest, "Request denied")
	}

}

// InformSubscribeFailed Informs the given callback address that their subscription request for given topic has been denied
func InformSubscribeFailed(callback, topic string) {
	u, r := url.Parse(callback)
	if r != nil {
		log.Println("error parsing url")
		return
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		log.Println("error parsing query")
		return
	}
	q.Add("hub.mode", "denied")
	q.Add("hub.topic", topic)
	q.Add("hub.reason", "Request was denied because the given arguments were not acceptable.")
	u.RawQuery = q.Encode()
	//create a request for verification
	req, erro := http.NewRequest("GET", u.String(), nil)
	if erro != nil {
		//handle it
		log.Println("new request error!")
		return
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept-Charset", "utf-8")

	err := req.ParseForm()
	if err != nil {
		return
	}

	//send the request
	_, er := client.Do(req)
	if er != nil {
		log.Println("req FAILED!")
		log.Println(er.Error())
		return
	}
}

// AttemptRegistration Function to validate and finish a subscription or unsubscription attempt
func AttemptRegistration(callback string, secret []byte, topic []byte, mode []byte) {

	//create a random string for verification of intent - hub.challenge value
	challenge := RandStringBytes(10)
	lease := 30 //lease time in seconds, temp value 86400s = 1 day, currently set to 30s to easily test subscriptions running out

	//parse the URL so that we can append some query string arguments
	u, r := url.Parse(callback)
	if r != nil {
		log.Println("error parsing url")
		return
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		log.Println("error parsing query")
		return
	}
	//query string arguments to append
	q.Add("hub.mode", string(mode))
	q.Add("hub.topic", string(topic))
	q.Add("hub.challenge", challenge)
	q.Add("hub.lease_seconds", strconv.Itoa(lease))
	u.RawQuery = q.Encode()
	//create a request for verification
	req, erro := http.NewRequest("GET", u.String(), nil)
	if erro != nil {
		log.Println("new request error!")
		return
	}
	//set headers for the request so that the receiver knows what to expect
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept-Charset", "utf-8")

	//send the request
	resp, er := client.Do(req)
	if er != nil {
		log.Println("req FAILED!")
		log.Println(er.Error())
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(resp.Body)
	//read the response body
	b, _ := ioutil.ReadAll(resp.Body)
	if string(b) != challenge { //check if returned body matches challenge for verification
		log.Println("Error: Returned challenge does not match")
		return
	}

	//make sure the request returned an OK status code
	if resp.StatusCode < 300 && resp.StatusCode >= 200 {
		if string(mode) == "unsubscribe" { //if the user wants to unsub
			subscribers.Remove(callback)
			return
		} else { //user wants to sub
			subscribers.Set(callback, subscription{
				callback:         callback,
				secret:           string(secret),
				topic:            string(topic),
				lease:            lease,
				subscriptionDate: time.Now(),
			})
			return
		}
	} else {
		log.Println("FAILURE! we could not subscribe! Request returned a non-OK status code!")
		return
	}
}

//letters to create a random string from
const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// RandStringBytes Creates a random string with n characters
func RandStringBytes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[rand.Intn(len(letterBytes))]
	}
	return string(b)
}

// Sign signs a message with a given key and returns the string value
func Sign(msg, key []byte) string {
	mac := hmac.New(sha256.New, key) //uses sha256 algorithm
	mac.Write(msg)
	return hex.EncodeToString(mac.Sum(nil))
}

// Publish called from /publish
//Sends some data to all current subscribers
func Publish(c echo.Context) error {
	//array to hold the subscribers which lease time has expired and should be removed
	subsToRemove := make([]string, 0, subscribers.Count())
	//some data which will be sent as the payload
	randData := []byte(`{"data":"THISISCOOLDATA"}`)
	for tuple := range subscribers.IterBuffered() { //loop through all key/value tuples found in the map holding subscribers
		key := tuple.Key
		value := tuple.Val.(subscription)
		//check if subscription has expired
		if getSecondsSinceSubscribed(value.subscriptionDate) > value.lease {
			subsToRemove = append(subsToRemove, key) //if enough time has passed so that the subscription has expired, add the key to a list of subs to remove and don't send json
		} else {
			PostJsonToSub(randData, value) //if within lease time, send json
		}
	}
	for i := 0; i < len(subsToRemove); i++ { //loop through all keys found to have expired lease time and remove them from the map
		subscribers.Remove(subsToRemove[i])
	}
	return c.String(http.StatusOK, "Published data to subscribers")
}

// PostJsonToSub posts given data as JSON to given subscription over http
func PostJsonToSub(data []byte, sub subscription) {
	req, err := http.NewRequest("POST", sub.callback, bytes.NewBuffer(data))
	if err != nil {
		log.Println("Error creating post request for sub: " + sub.callback + " error: " + err.Error())
		return
	}
	//Generate an HMAC signature of the payload
	hash := Sign(data, []byte(sub.secret))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Add("Content-Type", sub.topic)
	req.Header.Set("X-Hub-Signature", "sha256="+hash) //send the HMAC signature as a header - Algorithm is always sha256

	//Add link headers
	headers := "<http://hub:8080>; rel=\"hub\"," +
		"<" + sub.topic + ">; rel=\"self\""
	links := linkheader.Parse(headers)
	for _, link := range links {
		req.Header.Add("Link", link.String())
	}

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

// getSecondsSinceSubscribed help function to get time passed in seconds since given date
func getSecondsSinceSubscribed(subscribeDate time.Time) int {
	//get the current time and date
	currentTime := time.Now()
	//return the time passed since subscription till now in seconds
	return int(math.Abs(subscribeDate.Sub(currentTime).Seconds()))
}

// isInputCorrect help function to see if the given arguments are in a valid format
func isInputCorrect(callback, secret, topic, mode string) bool {
	if len(callback) > 0 && len(secret) > 0 && len(topic) > 0 && len(mode) > 0 { //check that no string is empty
		if mode == "subscribe" || mode == "unsubscribe" { //check if mode contains a valid string
			return true //if correct arguments return true
		}
	}

	return false //if one check fails, return false
}
