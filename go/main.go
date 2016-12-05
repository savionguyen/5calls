package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/gorilla/mux"
	_ "github.com/mattn/go-sqlite3"
	"github.com/patrickmn/go-cache"
)

var (
	addr         = flag.String("addr", ":8090", "[ip]:port to listen on")
	dbfile       = flag.String("dbfile", "fivecalls.db", "filename for sqlite db")
	airtableKey  = os.Getenv("AIRTABLE_API_KEY")
	civicKey     = os.Getenv("CIVIC_API_KEY")
	globalIssues = AirtableIssues{}

	db         *sql.DB
	countCache = cache.New(1*time.Hour, 10*time.Minute)
)

var pagetemplate *template.Template

func main() {
	flag.Parse()

	if airtableKey == "" {
		log.Fatal("No airtable API key found")
	}
	if civicKey == "" {
		log.Fatal("No google civic API key found")
	}

	// log.Printf("api keys %s %s", airtableKey, civicKey)
	refreshIssuesAndContacts()

	// index template... unused
	p, err := template.ParseFiles("index.html")
	if err != nil {
		log.Println("can't parse template:", err)
	}
	pagetemplate = p

	// open database
	db, err = sql.Open("sqlite3", fmt.Sprintf("./%s", *dbfile))
	if err != nil {
		log.Printf("can't open databse: %s", err)
		return
	}
	defer db.Close()

	// set up http routing
	r := mux.NewRouter()
	r.HandleFunc("/issues/{zip}", pageHandler)
	r.HandleFunc("/issues/", pageHandler)
	r.HandleFunc("/admin/", adminHandler)
	r.HandleFunc("/admin/refresh", adminRefreshHandler)
	r.HandleFunc("/report/", reportHandler)
	http.Handle("/", r)
	log.Printf("running fivecalls-web on port %v", *addr)

	log.Fatal(http.ListenAndServe(*addr, nil))
}

func pageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET only", 403)
		return
	}

	vars := mux.Vars(r)
	zip := vars["zip"]

	localContacts := []AirtableContact{}

	if len(zip) == 5 && zip != "" {
		log.Printf("zip %s", zip)

		googResponse, err := getReps(zip)
		if err != nil {
			panic(err)
		}

		// remove president and vice president from officials
		validOfficials := []GoogleOfficial{}
		for _, office := range googResponse.Offices {
			if strings.Contains(office.Name, "President") {
				continue
			}

			for _, index := range office.OfficialIndices {
				official := googResponse.Officials[index]

				if strings.Contains(office.Name, "Senate") {
					official.Area = "Senate"
				} else if strings.Contains(office.Name, "House") {
					official.Area = "House"
				} else {
					official.Area = "Other"
				}

				validOfficials = append(validOfficials, official)
			}
		}

		// swap to our own model for sane JSON
		for _, rep := range validOfficials {
			// fields := AirtableContact.Fields{Name: rep.Name, Phone: rep.Phones[0]}
			contact := AirtableContact{Fields: struct {
				Name     string
				Phone    string
				PhotoURL string
				Area     string
			}{
				Name:     rep.Name,
				Phone:    rep.Phones[0],
				PhotoURL: rep.PhotoURL,
				Area:     rep.Area,
			}}
			// contact := Contact{Name: rep.Name, Phone: rep.Phones[0], PhotoURL: rep.PhotoURL, Area: rep.Area}

			localContacts = append(localContacts, contact)
		}
	} else {
		log.Printf("no zip")
	}

	// add local reps where necessary
	customizedIssues := AirtableIssues{}
	for _, issue := range globalIssues.Records {
		for i, contact := range issue.Contacts {
			if contact.Fields.Name == "LOCAL REP" {
				// this is how you remove an item from a list in go :/
				issue.Contacts = append(issue.Contacts[:i], issue.Contacts[i+1:]...)
				// add the local contacts loaded from google civic
				issue.Contacts = append(issue.Contacts, localContacts...)
			}
		}

		customizedIssues.Records = append(customizedIssues.Records, issue)
	}

	jsonData, err := json.Marshal(customizedIssues.exportIssues())
	if err != nil {
		panic(err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(jsonData)
}
