package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/google/go-github/github"
	mailgun "github.com/mailgun/mailgun-go"
	"github.com/patrickmn/go-cache"
	"github.com/tadvi/rkv"
	"github.com/urfave/cli"
	"golang.org/x/oauth2"
)

type payload struct {
	Action string
	Issue  struct {
		ID     int
		Number int
		Title  string
		User   struct {
			Login, Url string
			ID         int
		}
		State string
		Body  string
	}
	Comment struct {
		User struct {
			Login, Url string
			ID         int
		}
		Body string
		ID   int
	}

	Repository struct {
		Name  string
		Owner struct {
			Login string
		}
	}
}

var (
	version   string
	builddate string
	gh_cache  *cache.Cache
	gh_client *github.Client
	ctx       context.Context
	mg        mailgun.Mailgun
	dryrun    bool
	kv        *rkv.Rkv
)

func main() {
	app := cli.NewApp()
	app.Name = "gh2mg"
	app.Usage = "Use github issues as an issue tracker, this allows sending emails from issue comments and making issue comments from received mails."
	app.Version = fmt.Sprintf("%s (%s)", version, builddate)

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:   "listen",
			Value:  "127.0.0.1:5000",
			Usage:  "Server Address",
			EnvVar: "LISTEN_ADDR",
		},
		cli.BoolFlag{
			Name:   "dryrun",
			Usage:  "Do not send any mails or submit any GitHub comments",
			EnvVar: "DRY_RUN",
		},
		cli.StringFlag{
			Name:   "kv",
			Usage:  "K/V Database Path",
			EnvVar: "KV_DB",
		},
		cli.StringFlag{
			Name:   "github",
			Usage:  "Github OAuth Token",
			EnvVar: "GITHUB_OAUTH_TOKEN",
		},
		cli.StringFlag{
			Name:   "mg_domain",
			EnvVar: "MAILGUN_DOMAIN",
		},
		cli.StringFlag{
			Name:   "mg_key",
			EnvVar: "MAILGUN_API_KEY",
		},
		cli.StringFlag{
			Name:   "mg_pubkey",
			EnvVar: "MAILGUN_API_PUBKEY",
		},
		cli.BoolFlag{
			Name:   "gdpr_compliance_mode",
			Usage:  "Enables GDPR Compliance mode which translates pseudonyms and templated variables into their real values before emailing",
			EnvVar: "GDPR_COMPLIANCE_MODE",
		},
	}

	app.Action = func(c *cli.Context) {
		// Globals
		kv, err := rkv.NewSafe(c.String("kv"))
		if err != nil {
			log.Fatal("Can not open database file")
		}
		//defer kv.Close()

		arr := kv.GetKeys("", -1)
		for _, key := range arr {
			var v int //v := Mytype{}
			err := kv.Get(key, &v)
			if err != nil {
				log.Fatal("Error while iterating %q", err.Error())

			}
			fmt.Println(key, v)

		}

		dryrun = c.Bool("dryrun")
		// Cache Setup
		gh_cache = cache.New(24*time.Hour, 48*time.Hour)
		// Github Setup
		ctx = context.Background()
		ts := oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: c.String("github")},
		)
		tc := oauth2.NewClient(ctx, ts)
		gh_client = github.NewClient(tc)
		// Mailgun Setup
		mg = mailgun.NewMailgun(
			c.String("mg_domain"),
			c.String("mg_key"),
			c.String("mg_pubkey"),
		)

		http.Handle("/github", http.HandlerFunc(githubWebHook))
		http.Handle("/mailgun", http.HandlerFunc(mailgunWebHook))

		log.Printf("listening for github  webhooks on: %s/github", c.String("listen"))
		log.Printf("listening for mailgun webhooks on: %s/mailgun", c.String("listen"))
		log.Fatal(http.ListenAndServe(c.String("listen"), nil))

	}

	err := app.Run(os.Args)
	if err != nil {
		panic(err)
	}

}

func getNameForUser(username string) (name string) {
	human_name_interface, found := gh_cache.Get(username)
	if found {
		human_name := human_name_interface.(string)
		// cast to string
		log.WithFields(log.Fields{
			"username":  username,
			"cached":    true,
			"humanname": human_name,
		}).Info("Fetching Username")
		return human_name
	}

	user, _, err := gh_client.Users.Get(ctx, username)
	if err != nil {
		log.Fatal(err)
	}
	log.WithFields(log.Fields{
		"username":  username,
		"cached":    false,
		"humanname": *user.Name,
	}).Info("Fetching Username")

	gh_cache.Set(username, *user.Name, cache.DefaultExpiration)
	return *user.Name
}

func extractEmailFromIssue(body string) (email string) {
	// <a href="mailto:'hxr+bugtest@hx42.org'"><span style="font-family: monospace;">'hxr+bugtest@hx42.org'</span>
	body_email_start := body[strings.Index(body, "<a href=\"mailto:'")+len("<a href=\"mailto:'"):]
	email_end := strings.Index(body_email_start, "</a>")
	html_email := body_email_start[0:email_end]
	email = html_email[:strings.Index(html_email, "'")]
	return email
}

func commentToEmail(author, title, comment, reply_to string) (messageID string) {
	log.WithFields(log.Fields{
		"author":   author,
		"title":    title,
		"reply_to": reply_to,
		"dry":      dryrun,
	}).Info("EMail Hanlder")

	if dryrun {
		return ""
	}

	message := mg.NewMessage(
		author+" <bugs@usegalaxy.eu>",
		"Re: "+title,
		comment,
		reply_to,
	)
	resp, id, err := mg.Send(message)
	if err != nil {
		log.Fatal(err)

	}

	log.WithFields(log.Fields{
		"id":   id,
		"resp": resp,
	}).Info("Mailgun")
	return id
}

func emailToComment(comment, in_reply_to string) {
	log.WithFields(log.Fields{
		"in_reply_to": in_reply_to,
		"dry":         dryrun,
	}).Info("Comment Hanlder")

	var issueNum int

	arr := kv.GetKeys("", -1)
	for _, key := range arr {
		var v int //v := Mytype{}
		err := kv.Get(key, &v)
		if err != nil {
			log.Fatal("Error while iterating %q", err.Error())

		}
		fmt.Println(key, v)

	}
	//err := kv.Get(in_reply_to, issueNum)
	//if err != nil {
	//log.Error(err)
	//}
	issueNum = 1

	//fmt.Println(issueNum, err)
	// get a ref to the issue object
	issue, _, err := gh_client.Issues.Get(ctx, "usegalaxy.eu", "issues", issueNum)
	if err != nil {
		log.Error(err)
	}
	fmt.Println(issue, err)

	if dryrun {
		return
	}

	c := &github.IssueComment{
		Body: &comment,
	}
	_, _, err = gh_client.Issues.CreateComment(ctx, "usegalaxy.eu", "issue-testing", issueNum, c)
	if err != nil {
		log.Error(err)
	}

	return
}

func mailgunWebHook(w http.ResponseWriter, req *http.Request) {
	// ensure request method is POST
	if req.Method != "POST" {
		http.Error(w, http.StatusText(405), 405)
		return
	}

	// read body data from request
	//body, err := ioutil.ReadAll(req.Body)
	//if err != nil {
	//log.Error("failed to read request body:", err)
	//http.Error(w, http.StatusText(500), 500)
	//return
	//}

	err := req.ParseForm()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	//log.Println("r.PostForm", req.PostForm)
	html_body, _ := req.PostForm["stripped-html"]
	from, _ := req.PostForm["From"]
	in_reply_to, _ := req.PostForm["In-Reply-To"]

	emailToComment(
		from[0]+" wrote: \n\n"+html_body[0],
		in_reply_to[0],
	)

	w.Write([]byte("OK"))
}

func githubWebHook(w http.ResponseWriter, req *http.Request) {
	// ensure request method is POST
	if req.Method != "POST" {
		http.Error(w, http.StatusText(405), 405)
		return
	}

	if req.Header.Get("X-GitHub-Event") == "ping" {
		log.Info("GitHub pinged us")
		http.Error(w, http.StatusText(200), 200)
		return
	}

	// ensure supported event type
	if req.Header.Get("X-GitHub-Event") != "issue_comment" {
		log.Error("github event not supported: ", req.Header.Get("X-GitHub-Event"))
		http.Error(w, http.StatusText(400), 400)
		return
	}

	// ensure json payload
	if !strings.Contains(req.Header.Get("Content-Type"), "json") {
		log.Error("only json payload is supported")
		http.Error(w, http.StatusText(406), 406)
		return
	}

	// read body data from request
	body, err := ioutil.ReadAll(req.Body)
	if err != nil {
		log.Error("failed to read request body:", err)
		http.Error(w, http.StatusText(500), 500)
		return
	}

	// read payload to struct
	var comment payload
	if err := json.Unmarshal(body, &comment); err != nil {
		log.Error("failed to unmarshal payload to struct:", err)
		http.Error(w, http.StatusText(500), 500)
		return
	}

	if comment.Action != "created" {
		log.Println("Deleting/Editing comments is not supported")
		http.Error(w, http.StatusText(200), 200)
		return
	}

	// find hook based on repository
	response := commentToEmail(
		getNameForUser(comment.Comment.User.Login),
		comment.Issue.Title,
		comment.Comment.Body,
		extractEmailFromIssue(comment.Issue.Body),
	)

	if !dryrun {
		if response != "" {
			kv.Put(response, &comment.Issue.Number)
		} else {
			log.Error("Response to message ID was nil but not dry-run")
		}
	}

	w.Write([]byte("OK"))
}
