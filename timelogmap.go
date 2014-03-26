package libgojira

import (
	"bytes"
	"fmt"
	"sort"
	"text/template"
	"time"
)

type TimeLog struct {
	Key     string
	Date    time.Time
	Seconds int
	Issue   *Issue
	Author  string
}

func (tl TimeLog) String() string {
	return fmt.Sprintf("%s : %s", tl.Key, tl.PrettySeconds())
}

func (tl TimeLog) PrettySeconds() string {
	return PrettySeconds(tl.Seconds)
}

func (tl TimeLog) Sprintf(format string) (string, error) {
	tltpl, err := template.New("tl").Parse(format)
	if err != nil {
		return "", err
	}
	var txt []byte
	txtbuff := bytes.NewBuffer(txt)
	tltpl.Execute(txtbuff, tl)
	return txtbuff.String(), nil
}

func PrettySeconds(seconds int) string {
	//This works because it's an integer division.
	hours := seconds / 3600
	minutes := (seconds - (hours * 3600)) / 60
	seconds = (seconds - (hours * 3600) - (minutes * 60))
	return fmt.Sprintf("%2dh %2dm %2ds", hours, minutes, seconds)

}

func (tl TimeLog) Percentage() string {
	if tl.Issue.OriginalEstimate == 0 {
		return "N/A"
	}
	return fmt.Sprintf("%2.2f%%", (tl.Issue.TimeSpent/tl.Issue.OriginalEstimate)*100)
}

func (tlm TimeLogMap) String() string {
	buf := bytes.NewBuffer([]byte{})
	for moment, timelogs := range tlm {
		buf.WriteString(fmt.Sprintf("  %v\n", moment))
		for _, timelog := range timelogs {
			buf.WriteString(fmt.Sprintf("    %v\n", timelog.PrettySeconds()))
		}
	}
	return buf.String()
}

type TimeLogMap map[time.Time][]TimeLog
type TimeSlice []time.Time

func (ts TimeSlice) Len() int {
	return len(ts)
}

func (ts TimeSlice) Swap(i, j int) {
	ts[i], ts[j] = ts[j], ts[i]
}

func (ts TimeSlice) Less(i, j int) bool {
	return ts[i].Before(ts[j])
}

func (tlm TimeLogMap) GetSortedKeys() []time.Time {
	times := make(TimeSlice, 0)
	for k, _ := range tlm {
		times = append(times, k)
	}
	sort.Sort(times)
	return times
}

func (tlm TimeLogMap) SumForKey(k time.Time) int {
	seconds := 0
	for _, v := range tlm[k] {
		seconds += v.Seconds
	}
	return seconds
}

func (tlm TimeLogMap) SumForMap() int {
	seconds := 0
	for k, _ := range tlm {
		seconds += tlm.SumForKey(k)
	}
	return seconds
}

func TimeLogForIssue(issue *Issue, issue_json interface{}) TimeLogMap {
	logs_for_times := TimeLogMap{}
	logs_json, _ := jsonWalker("fields/worklog/worklogs", issue_json)
	logs, ok := logs_json.([]interface{})
	if ok {
		for _, log := range logs {
			//We got good json and it's by our user
			authorjson, _ := jsonWalker("author/name", log)
			if author, ok := authorjson.(string); ok {
				dsjson, _ := jsonWalker("started", log)
				if date_string, ok := dsjson.(string); ok {
					//"2013-11-08T11:37:03.000-0500" <-- date format
					precise_time, _ := time.Parse(JIRA_TIME_FORMAT, date_string)

					date := time.Date(precise_time.Year(), precise_time.Month(), precise_time.Day(), 0, 0, 0, 0, precise_time.Location())
					secondsjson, _ := jsonWalker("timeSpentSeconds", log)
					seconds := int(secondsjson.(float64))
					if _, ok := logs_for_times[date]; !ok {
						logs_for_times[date] = make([]TimeLog, 0)
					}
					logs_for_times[date] = append(logs_for_times[date], TimeLog{issue.Key, date, seconds, issue, author})

				}
			}
		}
		return logs_for_times
	}
	return nil
}

const JIRA_TIME_FORMAT = "2006-01-02T15:04:05.000-0700"
