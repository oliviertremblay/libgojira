package libgojira

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"os"
	"regexp"
	"strings"

	"github.com/hoisie/mustache"
	"thezombie.net/oauth1a"
)

//Options available to the app.
type Options struct {
	User       string `short:"u" long:"user" description:"Your username"`
	Passwd     string `short:"p" long:"pass" description:"Your password" default-mask:"*******"`
	NoCheckSSL bool   `short:"n" long:"no-check-ssl" description:"Don't check ssl validity"`
	UseStdIn   bool   `long:"stdin"`

	Verbose  bool     `short:"v" long:"verbose" description:"Be verbose"`
	Projects []string `short:"j" long:"project"`

	Server          string `short:"s" long:"server" description:"Jira server (just the domain name)"`
	IncludeSubtasks bool   `short:"a" long:"subtasks" description:"When grabbing an issue, also grab its subtasks"`
}

var options Options

func SetOptions(opts Options) {
	options = opts
}

//Worker object in charge of communicating with Jira, wrapper to the API
type JiraClient struct {
	client       *http.Client
	User, Passwd string
	Server       string
	options      Options
	OAuthCfg     *oauth1a.UserConfig
	OAuthService *oauth1a.Service
}

func NewJiraClient(options Options) *JiraClient {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: options.NoCheckSSL},
	}
	//	options.Verbose = true

	jar, err := cookiejar.New(nil)
	if err != nil {
		log.Println(err)
	}
	client := &http.Client{Transport: tr, Jar: jar}
	return &JiraClient{client, options.User, options.Passwd, options.Server, options, nil, nil}

}

func (jc *JiraClient) GetClient() *http.Client {
	return jc.client
}

func (jc *JiraClient) AddComment(issueKey string, comment string) (err error) {
	b, err := json.Marshal(map[string]interface{}{"body": comment})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/%s/comment", jc.issueUrl(), issueKey)
	if jc.options.Verbose {
		fmt.Println(url)
	}
	r, err := jc.Post(url, "application/json", bytes.NewBuffer(b))

	if err != nil {
		return jc.printRespErr(r, err)
	}
	if r.StatusCode >= 400 {
		return jc.printRespErr(r, &JiraClientError{"Oops."})
	}
	return err
}

var numregex *regexp.Regexp = regexp.MustCompile("[0-9]+")

func numOnly(s string) (string, error) {
	result := numregex.FindString(s)
	if result == "" {
		return "", &JiraClientError{"Not a number"}
	}
	return result, nil
}

func (jc *JiraClient) DelWorkLog(issueKey string, worklog_id string) (err error) {
	return jc.delById("worklog", issueKey, worklog_id)
}

func (jc *JiraClient) DelComment(issueKey string, comment_id string) (err error) {
	return jc.delById("comment", issueKey, comment_id)
}

func (jc *JiraClient) delById(issueobject, issuekey, id string) (err error) {
	cid, err := numOnly(id)
	if err != nil {
		return &JiraClientError{fmt.Sprintf("Bad %s id", issueobject)}
	}
	r, err := jc.Delete(fmt.Sprintf("%s/%s/%s/%s", jc.issueUrl(), issuekey, issueobject, cid), "", nil)
	if err != nil {
		return jc.printRespErr(r, err)
	}
	return err
}

func (jc *JiraClient) GetComments(issueKey string) (err error) {

	return &JiraClientError{"Not implemented"}
}

func (jc *JiraClient) printRespErr(res *http.Response, err error) error {
	if jc.options.Verbose {
		fmt.Println("Status code: ", res.StatusCode)
	}
	s, _ := ioutil.ReadAll(res.Body)
	fmt.Println(string(s))
	return err
}

func (jc *JiraClient) DelAttachment(issueKey string, att_name string) (err error) {
	iss, err := jc.GetIssue(issueKey)
	if err != nil {
		return err
	}

	for _, att := range iss.Files {
		if att.name == att_name {
			res, err := jc.Delete(att.self, "", nil)
			if res.StatusCode == 404 {
				return &JiraClientError{"Not found"}
			}
			if res.StatusCode == 403 {
				return &JiraClientError{"Unauthorized"}
			}
			if jc.options.Verbose {
				fmt.Println(res.StatusCode)
				sb, _ := ioutil.ReadAll(res.Body)
				fmt.Println(string(sb))
			}

			if err != nil {
				return err
			}
			log.Println("File removed from issue!")
			return nil
		}
	}
	return &JiraClientError{"File not found"}

}

func (jc *JiraClient) Upload(issueKey string, file string) (err error) {
	// Prepare a form that you will submit to that URL.
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	// Add your image file
	f, err := os.Open(file)
	if err != nil {
		return
	}
	fi, err := os.Lstat(file)
	fw, err := w.CreateFormFile("file", fi.Name())
	if err != nil {
		return
	}
	if _, err = io.Copy(fw, f); err != nil {
		return
	}
	// Don't forget to close the multipart writer.
	// If you don't close it, your request will be missing the terminating boundary.
	w.Close()

	// Now that you have a form, you can submit it to your handler.

	res, err := jc.Post(fmt.Sprintf("https://%s/rest/api/2/issue/%s/attachments", jc.Server, issueKey), w.FormDataContentType(), &b)

	if err != nil {
		s, _ := ioutil.ReadAll(res.Body)
		fmt.Println(string(s))
		return err
	}
	fmt.Println("File uploaded!")
	return nil
}

//Represents search options to Jira
type SearchOptions struct {
	Projects      []string //Limit search to a specific project
	CurrentSprint bool     //Limit search to stories in current sprint
	Open          bool     //Limit search to open issues
	Issue         string   //Limit search to a single issue
	JQL           string   //Pure JQL query, has precedence over any other option
	Type          []string
	NotType       []string
	Status        []string
	NotStatus     []string
}

func (ja *JiraClient) Search(searchoptions *SearchOptions) ([]*Issue, error) {
	var jqlstr string
	if searchoptions.JQL == "" {
		jql := make([]string, 0)
		if searchoptions.CurrentSprint {
			jql = append(jql, "sprint+in+openSprints()")
		}
		if searchoptions.Open {
			jql = append(jql, "status+=+'open'")
		}
		if searchoptions.Issue != "" {
			searchoptions.Issue = strings.Replace(searchoptions.Issue, " ", "+", -1)
			jql = append(jql, fmt.Sprintf("issue+=+'%s'+or+parent+=+'%s'", searchoptions.Issue, searchoptions.Issue))
		}
		if len(searchoptions.Projects) > 0 {
			searchoptions.Projects = searchoptions.Projects //strings.Replace(strings.searchoptions.Project, " ", "+", -1)
			jql = append(jql, fmt.Sprintf("project+in+('%s')", strings.Replace(strings.Join(searchoptions.Projects, "','"), " ", "+", -1)))
		}
		if len(searchoptions.Type) > 0 {
			jql = append(jql, strings.Replace(fmt.Sprintf("type+in+('%s')", strings.Join(searchoptions.Type, "','")), " ", "+", -1))
		}
		if len(searchoptions.NotType) > 0 {
			jql = append(jql, strings.Replace(fmt.Sprintf("type+not+in+(%s)", strings.Join(searchoptions.NotType, ",")), " ", "+", -1))
		}
		if len(searchoptions.Status) > 0 {
			jql = append(jql, strings.Replace(fmt.Sprintf("status+in+('%s')", strings.Join(searchoptions.Status, "','")), " ", "+", -1))
		}
		if len(searchoptions.NotStatus) > 0 {
			jql = append(jql, strings.Replace(fmt.Sprintf("status+not+in+('%s')", strings.Join(searchoptions.NotStatus, "','")), " ", "+", -1))
		}

		jqlstr = strings.Join(jql, "+AND+") + "+order+by+rank"
	} else {
		jqlstr = strings.Replace(searchoptions.JQL, " ", "+", -1)
	}
	url := fmt.Sprintf("https://%s/rest/api/2/search?jql=%s&fields=*all", ja.Server, jqlstr)
	if ja.options.Verbose {
		fmt.Println(url)
	}
	resp, err := ja.Get(url)
	if err != nil {
		if resp != nil {
			fmt.Println(resp.StatusCode)
			b, _ := ioutil.ReadAll(resp.Body)
			fmt.Println(string(b))
		}
		return nil, err
	}
	if resp.StatusCode >= 300 {
		fmt.Println(resp.StatusCode)
		b, _ := ioutil.ReadAll(resp.Body)
		fmt.Println(string(b))

		return nil, &JiraClientError{resp.Status}
	}

	obj, err := JsonToInterface(resp.Body)
	if err != nil {
		return nil, err
	}
	issues, _ := jsonWalker("issues", obj)
	issuesSlice, ok := issues.([]interface{})

	if !ok {
		issuesSlice = []interface{}{}
	}
	result := []*Issue{}
	for _, v := range issuesSlice {
		iss, err := ja.NewIssueFromIface(v)
		if err == nil {
			result = append(result, iss)
		}
		if err != nil {
			fmt.Println(err)
		}

	}

	return result, nil
}

func (jc *JiraClient) NewIssueFromIface(obj interface{}) (*Issue, error) {
	issue := new(Issue)
	key, err := jsonWalker("key", obj)
	if err != nil {
		return nil, err
	}
	issuetype, err := jsonWalker("fields/issuetype/name", obj)
	if err != nil {
		return nil, err
	}
	summary, err := jsonWalker("fields/summary", obj)
	if err != nil {
		return nil, err
	}

	//Is optional
	parentJS, _ := jsonWalker("fields/parent/key", obj)
	var parent string
	parent, _ = parentJS.(string)
	if err != nil {
		parent = ""
	}

	//Following three things are optional
	descriptionjs, _ := jsonWalker("fields/description", obj)
	statusjs, _ := jsonWalker("fields/status/name", obj)
	assigneejs, _ := jsonWalker("fields/assignee/name", obj)

	ok, ok2, ok3 := true, true, true
	issue.Key, ok = key.(string)
	issue.Parent = parent
	issue.Summary, ok2 = summary.(string)
	issue.Type, ok3 = issuetype.(string)
	issue.Description, _ = descriptionjs.(string)
	issue.Status, _ = statusjs.(string)
	issue.Assignee, _ = assigneejs.(string)
	issue.Files = getFileListFromIface(obj)
	issue.Points, _ = grabCustomField("customfield_10003", obj)
	if !(ok && ok2 && ok3) {
		return nil, newIssueError("Bad Issue")
	}
	if issue.Type != "Sub-task" {
		OriginalEstimateJs, err := jsonWalker("fields/aggregatetimeoriginalestimate", obj)
		if err != nil {
			return nil, err
		}
		TimeSpentJs, err := jsonWalker("fields/aggregatetimespent", obj)
		if err != nil {
			return nil, err
		}

		RemainingEstimateJs, err := jsonWalker("fields/aggregatetimeestimate", obj)
		if err != nil {
			return nil, err
		}

		issue.OriginalEstimate, _ = OriginalEstimateJs.(float64)
		issue.TimeSpent, _ = TimeSpentJs.(float64)
		issue.RemainingEstimate, _ = RemainingEstimateJs.(float64)
		if jc.options.IncludeSubtasks {
			subtasksJS, err := jsonWalker("fields/subtasks", obj)
			st := []*Issue{}
			if subtasks, ok := subtasksJS.([]interface{}); ok && err == nil {
				for _, subtask := range subtasks {
					k, _ := jsonWalker("key", subtask)
					i, _ := jc.GetIssue(k.(string))
					st = append(st, i)
				}
				issue.SubTasks = st

			}
		}
	} else {
		OriginalEstimateJs, err := jsonWalker("fields/timeoriginalestimate", obj)
		if err != nil {
			return nil, err
		}
		RemainingEstimateJs, err := jsonWalker("fields/timeremainingestimate", obj)
		if err != nil {
			return nil, err
		}
		TimeSpentJs, err := jsonWalker("fields/timespent", obj)
		if err != nil {
			return nil, err
		}

		issue.OriginalEstimate, _ = OriginalEstimateJs.(float64)
		issue.RemainingEstimate, _ = RemainingEstimateJs.(float64)
		issue.TimeSpent, _ = TimeSpentJs.(float64)
	}
	issue.TimeLog = TimeLogForIssue(issue, obj)
	comms, err := jsonWalker("fields/comment/comments", obj)
	if err == nil {
		issue.Comments = commentsFromIFace(comms)
		if options.Verbose {
			fmt.Println(issue.Comments)
		}
	} else {
		if options.Verbose {
			fmt.Println(err)

		}
		issue.Comments = CommentList{}
		return nil, err
	}

	return issue, nil
}

func grabCustomField(fieldname string, obj interface{}) (string, error) {
	ifields, err := jsonWalker("fields", obj)
	if err != nil {
		return "", err
	}
	fields := ifields.(map[string]interface{})
	for k, v := range fields {
		if k == fieldname {
			return fmt.Sprintf("%v", v), nil
		}
	}
	return "", errors.New("Field not found")
}

func commentsFromIFace(obj interface{}) CommentList {
	result := CommentList{}
	if comments, ok := obj.([]interface{}); ok {
		for _, cmj := range comments {
			if cm, ok := cmj.(map[string]interface{}); ok {
				if id, ok2 := cm["id"].(string); ok2 {
					if body, ok3 := cm["body"].(string); ok3 {
						if author, ok := cm["author"].(map[string]interface{})["displayName"].(string); ok {
							result = append(result, &Comment{Id: id, Body: body, AuthorName: author})
						}
					}

				}
			}
		}
	}
	return result
}

func getFileListFromIface(obj interface{}) IssueFileList {
	rez := make(IssueFileList, 0)
	attachmentsjs, err := jsonWalker("fields/attachment", obj)
	if err != nil {
		return rez
	}
	attachments, ok := attachmentsjs.([]interface{})
	if !ok {
		return rez
	}

	for _, v := range attachments {
		filename, err := jsonWalker("filename", v)
		file, err := jsonWalker("content", v)
		self_js, err := jsonWalker("self", v)
		if err != nil {
			continue
		}
		filenamestr, ok := filename.(string)
		filestring, ok2 := file.(string)
		self, ok3 := self_js.(string)
		if ok && ok2 && ok3 {
			rez = append(rez, &IssueFile{name: filenamestr, url: filestring, self: self})
		}
	}
	return rez
}

func (jc *JiraClient) GetIssue(issueKey string) (*Issue, error) {

	resp, err := jc.Get(fmt.Sprintf("https://%s/rest/api/2/issue/%s", jc.Server, issueKey))
	if err != nil {
		panic(err)
	}
	obj, err := JsonToInterface(resp.Body)
	iss, err := jc.NewIssueFromIface(obj)
	if err != nil {
		return nil, err
	}
	return iss, nil
}

func tagsFromStringSlice(tags []string) []interface{} {
	tags_obj := make([]interface{}, 0)
	for _, tag := range tags {
		tags_obj = append(tags_obj, map[string]interface{}{"add": tag})
	}
	return tags_obj
}

func (jc *JiraClient) AddTags(issuekey string, tags []string) error {
	postjs := map[string]interface{}{"labels": tagsFromStringSlice(tags)}

	return jc.UpdateIssue(issuekey, postjs)

}

func (jc *JiraClient) UpdateIssue(issuekey string, postjs map[string]interface{}) error {
	postdata, err := json.Marshal(map[string]interface{}{"update": postjs})

	if err != nil {
		return err
	}
	resp, err := jc.Put(fmt.Sprintf("https://%s/rest/api/latest/issue/%s", jc.Server, issuekey), "application/json", bytes.NewBuffer(postdata))

	if err != nil {
		return err
	}
	if resp.StatusCode != 204 {
		log.Println(resp.StatusCode)
		return &JiraClientError{"Bad request"}
	}
	log.Println(fmt.Sprintf("Issue %s updated!", issuekey))
	return nil
}

func (jc *JiraClient) Client() *http.Client {
	return jc.client
}

func (jc *JiraClient) Get(url string) (*http.Response, error) {
	req, err := jc.newRequest("GET", url, "", nil)
	if err != nil {
		return nil, err
	}
	return jc.client.Do(req)
}

func (jc *JiraClient) Post(url, mimetype string, rdr io.Reader) (*http.Response, error) {
	req, err := jc.newRequest("POST", url, mimetype, rdr)
	req.Header.Add("X-Atlassian-Token", "nocheck")
	if err != nil {
		return nil, err
	}
	return jc.client.Do(req)
}

func (jc *JiraClient) Put(url, mimetype string, rdr io.Reader) (*http.Response, error) {
	req, err := jc.newRequest("PUT", url, mimetype, rdr)
	if err != nil {
		return nil, err
	}
	return jc.client.Do(req)
}

func (jc *JiraClient) Delete(url, mimetype string, rdr io.Reader) (*http.Response, error) {
	req, err := jc.newRequest("DELETE", url, mimetype, nil)
	if err != nil {
		return nil, err
	}
	return jc.client.Do(req)
}

func (jc *JiraClient) newRequest(verb, url, mimetype string, rdr io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(verb, url, rdr)
	if err != nil {
		return nil, err
	}
	if mimetype != "" {
		req.Header.Add("Content-Type", mimetype)
	}
	if jc.OAuthCfg == nil {
		req.SetBasicAuth(jc.User, jc.Passwd)
	} else {
		jc.OAuthService.Sign(req, jc.OAuthCfg)
	}
	return req, nil
}

type JiraClientError struct {
	msg string
}

func (jce *JiraClientError) Error() string {
	return jce.msg
}

//Helper function to read a json input and unmarshal it to an interface{} object
func JsonToInterface(reader io.Reader) (interface{}, error) {
	rdr := bufio.NewReader(reader)
	js := make([]string, 0)
	for {
		s, err := rdr.ReadString('\n')
		js = append(js, s)
		if err != nil {
			break
		}

	}
	njs := strings.Join(js, "")
	var obj interface{}
	err := json.Unmarshal([]byte(njs), &obj)
	if err != nil {
		return nil, err
	}
	return obj, nil
}

//Helper function to navigate an unmarshalled json interface{} object.
//Takes in a path in the form of "path/to/field".
//Doesn't deal with arrays.
func jsonWalker(path string, json interface{}) (interface{}, error) {
	p := strings.Split(path, "/")
	tmpval := json
	for i, subpath := range p {
		submap, ok := tmpval.(map[string]interface{})
		if !ok {
			return nil, errors.New(fmt.Sprintf("Bad path, %s is not a map[string]interface{}", p[i-1]))
		}
		if i < (len(p) - 1) {
			tmpval = submap[subpath]
		} else {
			return submap[subpath], nil
		}
	}
	return nil, errors.New("Woooops")
}

func (jc *JiraClient) GetTaskTypes() (map[string]map[string]string, error) {
	resp, err := jc.Get(fmt.Sprintf("https://%s/rest/api/2/issue/createmeta", jc.Server))
	if err != nil {
		return nil, err
	}
	obj, err := JsonToInterface(resp.Body)
	if err != nil {
		return nil, err
	}
	projs, err := jsonWalker("projects", obj)
	if err != nil {
		return nil, err
	}
	if probjs, ok := projs.([]interface{}); ok {
		projmap := map[string]map[string]string{}
		for _, v := range probjs {
			projnamejs, _ := jsonWalker("name", v)
			projkeyjs, _ := jsonWalker("key", v)
			projkey, _ := projkeyjs.(string)
			if projname, ok := projnamejs.(string); ok {
				projmap[projname] = map[string]string{}
				if projkey != "" {
					projmap[projkey] = projmap[projname]
				}
				issuesjs, _ := jsonWalker("issuetypes", v)
				if issues, ok := issuesjs.([]interface{}); ok {
					for _, issuetype := range issues {
						typenamejs, err := jsonWalker("name", issuetype)
						if err != nil {
							continue
						}
						if typename, ok := typenamejs.(string); ok {
							projmap[projname][strings.Replace(strings.ToLower(typename), " ", "-", -1)] = typename
							//							if projkey != "" {
							//								projmap[projkey][strings.Replace(strings.ToLower(typename), " ", "-", -1)] = typename
							//							}
						}
					}
				}
			}
		}
		if jc.options.Verbose {
			fmt.Println(projmap)
		}
		return projmap, nil
	}

	return map[string]map[string]string{}, nil
}

func (jc *JiraClient) GetProjList() ([]string, error) {
	resp, err := jc.Get(fmt.Sprintf("https://%s/rest/api/2/project", jc.Server))
	if err != nil {
		return nil, err
	}
	obj, err := JsonToInterface(resp.Body)
	if err != nil {
		return nil, err
	}
	result := []string{}
	if projs, ok := obj.([]interface{}); ok {
		for _, p := range projs {
			result = append(result, p.(map[string]interface{})["key"].(string))
		}
	}
	if jc.options.Verbose {
		log.Println(result)
	}
	return result, nil
}

func (jc *JiraClient) GetProjects() (map[string]JiraProject, error) {
	projmap := map[string]JiraProject{}
	resp, err := jc.Get(fmt.Sprintf("https://%s/rest/api/2/issue/createmeta", jc.Server))
	if err != nil {
		return nil, err
	}
	obj, err := JsonToInterface(resp.Body)
	if err != nil {
		return nil, err
	}
	projs, err := jsonWalker("projects", obj)
	if err != nil {
		return nil, err
	}
	if probjs, ok := projs.([]interface{}); ok {
		for _, v := range probjs {
			projnamejs, _ := jsonWalker("name", v)
			projkeyjs, _ := jsonWalker("key", v)
			projidjs, _ := jsonWalker("id", v)
			projname, _ := projnamejs.(string)
			projkey, _ := projkeyjs.(string)
			projid, _ := projidjs.(string)
			projmap[projname] = JiraProject{Id: projid, Name: projname, Key: projkey}
			projmap[projkey] = projmap[projname]
		}
	}

	return projmap, nil

}

func (jc *JiraClient) GetTaskType(friendlyname string) (string, error) {
	projmap, err := jc.GetTaskTypes()
	if err != nil {
		return "", err
	}

	if taskname, ok := projmap[jc.options.Projects[0]][friendlyname]; ok {
		return taskname, nil
	} else {
		if jc.options.Verbose {
			fmt.Println(projmap[jc.options.Projects[0]])
		}

	}

	return "", &JiraClientError{fmt.Sprintf("Task name not found for friendly name %s.", friendlyname)}
}

func (jc *JiraClient) CreateTask(project string, nto *NewTaskOptions) error {
	tt, err := jc.GetTaskType(nto.TaskType)
	if err != nil {
		return err
	}
	projmap, err := jc.GetProjects()
	if err != nil {
		return err
	}

	fields := map[string]interface{}{
		"summary":   nto.Summary,
		"project":   map[string]interface{}{"key": projmap[project].Key},
		"issuetype": map[string]interface{}{"name": tt}}
	if nto.Parent != nil {
		fields["parent"] = map[string]interface{}{"key": nto.Parent.Key}
	}
	if nto.Description != "" {
		fields["description"] = nto.Description
	}

	if len(nto.Labels) > 0 {
		fields["labels"] = nto.Labels //tagsFromStringSlice(nto.Labels)
	}
	if nto.OriginalEstimate != "" {
		fields["timetracking"] = map[string]string{"originalEstimate": nto.OriginalEstimate}
	}
	for _, field := range nto.Fields {
		split_f := strings.Split(field, "=")
		if len(split_f) < 2 {
			continue
		}
		fname := split_f[0]
		fval := strings.Join(split_f[1:], "=")
		fields[fname] = fval
	}
	for _, field := range nto.SelectFields {
		split_f := strings.Split(field, "=")
		if len(split_f) < 2 {
			continue
		}
		fname := split_f[0]
		fval := strings.Join(split_f[1:], "=")
		fields[fname] = map[string]interface{}{"value": fval}
	}

	iss, err := json.Marshal(map[string]interface{}{
		"fields": fields})
	if err != nil {
		return err
	}
	if jc.options.Verbose {
		fmt.Println(string(iss))
	}
	resp, err := jc.Post(fmt.Sprintf("https://%s/rest/api/2/issue", jc.Server), "application/json", bytes.NewBuffer(iss))
	if err != nil {
		return err
	}
	s, _ := ioutil.ReadAll(resp.Body)
	if resp.StatusCode != 201 {

		return &IssueError{fmt.Sprintf("%d: %s", resp.StatusCode, string(s))}
	}
	var js interface{}
	err = json.Unmarshal(s, &js)
	if err != nil {
		return err
	}
	keyjs, _ := jsonWalker("key", js)
	key, _ := keyjs.(string)
	log.Println(fmt.Sprintf("%s successfully created!", key))
	return nil
}

func (jc *JiraClient) issueUrl() string {
	return fmt.Sprintf("https://%s/rest/api/2/issue", jc.Server)
}

func PrintHtml(issues []*Issue) ([]byte, error) {
	var bs []byte
	out := bytes.NewBuffer(bs)
	var tmpl *mustache.Template
	tmpl, err := mustache.ParseString(defaultTemplate)

	if err != nil {
		return out.Bytes(), err
	}
	fmt.Fprintln(out, tmpl.Render(map[string]interface{}{"Issues": issues}))
	return out.Bytes(), err

}

type JiraProject struct {
	Name string
	Key  string
	Id   string
}

type NewTaskOptions struct {
	TaskType         string
	Summary          string
	OriginalEstimate string
	Parent           *Issue
	Fields           []string
	SelectFields     []string
	Labels           []string
	Description      string
}

func (jc *JiraClient) ChangeRank(rankthese []string, before_or_after string, target string) error {
	b := bytes.NewBuffer([]byte{})
	enc := json.NewEncoder(b)
	//	keys := make([]string, 0, len(rankthese))
	//	for _, i := range rankthese {
	//		keys = append(keys, i.Key)
	//	}
	var b_o_f string
	switch strings.ToLower(before_or_after) {
	case "before":
		b_o_f = "rankBeforeKey"
	case "after":
		b_o_f = "rankAfterKey"
	default:
		return fmt.Errorf("before_or_after needs to be set to either 'before' or 'after'.")
	}
	err := enc.Encode(map[string]interface{}{"issueKeys": rankthese, "customFieldId": 10560, b_o_f: target})
	fmt.Println(b.String())
	if err != nil {
		return err
	}
	res, err := jc.Put(fmt.Sprintf("https://%s/rest/greenhopper/1.0/api/rank/%s/", jc.Server, before_or_after), "application/json", b)
	if err != nil {
		return err
	}
	if res.StatusCode >= 400 {
		msg, err := ioutil.ReadAll(res.Body)
		if err != nil {
			return nil
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
