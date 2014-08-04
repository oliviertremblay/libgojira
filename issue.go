package libgojira

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os/exec"
	"regexp"
	"strings"
)

//Representation of a single issue
type Issue struct {
	Key               string
	Type              string
	Summary           string
	Parent            string
	Description       string
	Status            string
	Assignee          string
	Files             IssueFileList
	OriginalEstimate  float64
	RemainingEstimate float64
	TimeSpent         float64
	Comments          CommentList
	TimeLog           TimeLogMap
	Updated           string
	Points            string
	SubTasks          []*Issue
}

func (i *Issue) QRCodeBase64() string {
	cmd := exec.Command("qrencode", "-o", "-", "-s", "2", i.Url())
	b, e := cmd.Output()
	if e != nil {
		panic(e)
	}
	cmd2 := exec.Command("base64")
	cmd2.Stdin = bytes.NewBuffer(b)
	b2, e := cmd2.Output()
	if e != nil {
		panic(e)
	}
	return string(b2)
}

func (i *Issue) ETag() string {
	hash := sha256.New()
	io.WriteString(hash, i.Updated)
	return string(hash.Sum(nil))
}

type CommentList []*Comment

type Comment struct {
	Id         string
	Body       string
	AuthorName string
}

func (cm *Comment) String() string {
	return fmt.Sprintf("\t#%s by %s: \n\n%s\n", cm.Id, cm.AuthorName, cm.Body)
}

func (cl CommentList) String() string {
	var s string
	for _, v := range cl {
		s += fmt.Sprintln(fmt.Sprintf("\t%v", v))
	}
	return s
}

type IssueFileList []*IssueFile

type IssueFile struct {
	name string
	url  string
	self string
}

func (issf *IssueFile) String() string {
	return fmt.Sprintf("%s : %s", issf.name, issf.url)
}

func (ifl IssueFileList) String() string {
	var s string
	for _, v := range ifl {
		s += fmt.Sprintln(fmt.Sprintf("\t%v", v))
	}
	return s
}

func (i *Issue) String() string {
	p := ""
	if i.Parent != "" {
		p = fmt.Sprintf(" of %s", i.Parent)
	}
	return fmt.Sprintf("%s (%s%s): %s", i.Key, i.Type, p, i.Summary)
}

func (i *Issue) Url() string {
	return fmt.Sprintf("https://%s/browse/%s", Server, i.Key)
}

var Server string

func (i *Issue) PrettySprint() string {
	sa := make([]string, 0)
	sa = append(sa, fmt.Sprintln(i.String()))
	sa = append(sa, fmt.Sprintln(fmt.Sprintf("Jira URL: %s", i.Url())))
	sa = append(sa, fmt.Sprintln(fmt.Sprintf("Status: %s", i.Status)))
	sa = append(sa, fmt.Sprintln(fmt.Sprintf("Assignee: %s", i.Assignee)))
	sa = append(sa, fmt.Sprintln(fmt.Sprintf("Original time estimate: %s", PrettySeconds(int(i.OriginalEstimate)))))
	sa = append(sa, fmt.Sprintln(fmt.Sprintf("Time spent: %s", PrettySeconds(int(i.TimeSpent)))))
	sa = append(sa, fmt.Sprintln(fmt.Sprintf("Remaining time estimated: %s", PrettySeconds(int(i.RemainingEstimate)))))
	r, _ := regexp.Compile("[*]([^*]*)[*]")
	splitdesc := strings.Split(i.Description, "\n")
	for k, v := range splitdesc {
		splitdesc[k] = r.ReplaceAllString(v, "\x1b[1m$1\x1b[22m")
	}
	desc := strings.Join(splitdesc, "\n")
	desc = strings.Replace(desc, "{color:red}", "\x1b[31m", -1)
	desc = strings.Replace(desc, "{color}", "\x1b[39m", -1)

	r2, _ := regexp.Compile("(?s)[{]quote[}](.*)[{]quote[}]")
	desc = r2.ReplaceAllString(desc, "\x1b[51$1\x1b[54")
	sa = append(sa, fmt.Sprintln(fmt.Sprintf("Description: %s", desc)))
	if len(i.Files) > 0 {
		sa = append(sa, fmt.Sprintln(fmt.Sprintf("Files: \n%v", i.Files)))
	}

	if len(i.Comments) > 0 {
		sa = append(sa, fmt.Sprintln(fmt.Sprintf("Comments: \n%v", i.Comments)))
	}

	if len(i.TimeLog) > 0 {
		sa = append(sa, fmt.Sprintln(fmt.Sprintf("Worklog: \n%v", i.TimeLog)))
	}

	return strings.Join(sa, "\n")
}

func (i *Issue) Assign(author string, jc *JiraClient) error {
	js, err := json.Marshal(map[string]interface{}{"name": author})
	if err != nil {
		return err
	}
	resp, err := jc.Put(fmt.Sprintf("https://%s/rest/api/2/issue/%s/assignee", jc.options.Server, i.Key), "application/json", bytes.NewBuffer(js))
	if err != nil {
		return err
	}
	if resp.StatusCode != 204 {
		s, _ := ioutil.ReadAll(resp.Body)
		return &IssueError{fmt.Sprintf("%d: %s", resp.StatusCode, string(s))}
	}
	return nil
}

func (i *Issue) StartProgress(jc *JiraClient) error {
	id, err := i.getTransitionId("start", jc)
	if err != nil {
		return err
	}
	err = i.doTransition(id, jc)
	if err != nil {
		return err
	}

	return nil
}

func (i *Issue) StopProgress(jc *JiraClient) error {
	id, err := i.getTransitionId("stop", jc)
	if err != nil {
		return err
	}
	err = i.doTransition(id, jc)
	if err != nil {
		return err
	}
	return nil
}

func capitalize(str string) string {

	splitstr := strings.Split(str, " ")
	for i, substr := range splitstr {
		cap := false
		s2 := ""
		for _, k := range substr {
			if !cap {
				s2 += strings.ToUpper(string(k))
				cap = true
			} else {
				s2 += strings.ToLower(string(k))
			}

		}
		splitstr[i] = s2
	}
	return strings.Join(splitstr, " ")
}

type Resolutions []string

func (r Resolutions) String() string {
	res := ""
	for _, v := range r {
		res += fmt.Sprintln(v)
	}
	return res
}

func (i *Issue) PossibleResolutions(jc *JiraClient) (Resolutions, error) {
	resp, err := jc.Get(fmt.Sprintf("https://%s/rest/api/2/issue/%s/transitions?expand=transitions.fields", jc.Server, i.Key))
	if err != nil {
		return nil, err
	}
	obj, err := JsonToInterface(resp.Body)
	if err != nil {
		return nil, err
	}

	txs, err := jsonWalker("transitions", obj)
	if err != nil {
		return nil, err
	}
	result := Resolutions{}
	for _, tx := range txs.([]interface{}) {
		name, _ := jsonWalker("name", tx)
		if strings.Contains(name.(string), "Resolve") {
			txRes, err := jsonWalker("fields/resolution/allowedValues", tx)
			if err == nil {
				for _, singleRes := range txRes.([]interface{}) {
					n, _ := jsonWalker("name", singleRes)
					result = append(result, strings.ToLower(n.(string)))
				}
			}
		}
	}
	return result, nil
}

func (i *Issue) ResolveIssue(jc *JiraClient, resolution string) error {
	id, err := i.getTransitionId("resolve", jc)
	if err != nil {
		return err
	}
	err = i.doTransitionWithFields(id, map[string]interface{}{"resolution": map[string]interface{}{"name": capitalize(resolution)}}, jc)
	if err != nil {
		res, _ := i.PossibleResolutions(jc)
		return newIssueError(fmt.Sprintf("Command failed. Possible resolution values include: \n%sOriginal Error: %s", res, err.Error()))
	}
	return nil
}

func (i *Issue) doTransitionWithFields(id string, fields interface{}, jc *JiraClient) error {
	putJs, err := json.Marshal(map[string]interface{}{"transition": map[string]interface{}{"id": id}, "fields": fields})
	if err != nil {
		return err
	}
	resp, err := jc.Post(fmt.Sprintf("https://%s/rest/api/2/issue/%s/transitions", jc.options.Server, i.Key), "application/json", bytes.NewBuffer(putJs))
	if resp.StatusCode != 204 {
		s, _ := ioutil.ReadAll(resp.Body)
		return &IssueError{fmt.Sprintf("%d: %s", resp.StatusCode, string(s))}
	}
	return nil

}

func (i *Issue) doTransition(id string, jc *JiraClient) error {
	return i.doTransitionWithFields(id, nil, jc)
}

func (i *Issue) getTransitionId(transition string, jc *JiraClient) (string, error) {
	resp, err := jc.Get(fmt.Sprintf("https://%s/rest/api/2/issue/%s/transitions", jc.options.Server, i.Key))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		s, _ := ioutil.ReadAll(resp.Body)
		return "", &IssueError{fmt.Sprintf("%d: %s", resp.StatusCode, string(s))}
	}
	js, err := JsonToInterface(resp.Body)
	if err != nil {
		return "", err
	}
	transitions, err := jsonWalker("transitions", js)
	if err != nil {
		return "", err
	}
	for _, v := range transitions.([]interface{}) {
		name, _ := jsonWalker("name", v)
		if n, ok := name.(string); ok && strings.Contains(strings.ToLower(n), transition) {
			tid, _ := jsonWalker("id", v)
			return tid.(string), nil

		}
	}
	return "", &IssueError{"Transition ID not found"}

}

type IssueError struct {
	message string
}

func (ie *IssueError) Error() string {
	return ie.message
}

func newIssueError(msg string) *IssueError {
	return &IssueError{msg}
}
