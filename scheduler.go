package main

import (
	"auto_updater"
	"cron_eval"
	"database/sql"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"os/exec"
	"parse_update_script"
	"path/filepath"
	"strconv"
	"time"
	"util"
)

type UpdateScriptResponse struct {
	ret_code      int
	system_id     int
	stdout        string
	stderr        string
	is_start      bool
	is_end        bool
	update_script *parse_update_script.UpdateScript
	db            *sql.DB
	api_url       string
}

// SystemReboot executes a shell command to reboot the host
func SystemReboot() {
	// Sleep for 2 seconds to give time for the API post
	time.Sleep(2 * time.Second)
	cmd := exec.Command("/sbin/shutdown", "-r", "now")
	err := cmd.Run()
	if err != nil {
		panic(err)
	}
}

// Main entry point
func main() {
	log := auto_updater.GetLogger()

	exec_path, _ := os.Getwd()
	HOSTNAME, _ := os.Hostname()
	config, config_err := auto_updater.ParseConfig()
	if config_err != nil {
		log.Error("Unable to open config file")
	}
	log.Debug("Config Loaded")
	log.Debug(HOSTNAME)
	var DB_FILE = fmt.Sprintf("%s/%s", exec_path, config.Main.Dbfile)
	log.Debug("DB_FILE: ", DB_FILE)
	var CRONFILE = fmt.Sprintf("%s/%s", exec_path, config.Main.Cronfile)
	var SCRIPTPATH = fmt.Sprintf("%s/%s", exec_path, config.Main.Scriptpath)
	var APIURL = config.Main.APIUrl
	system_id, system_id_err := GetSystemId(APIURL, HOSTNAME)
	if system_id_err != nil {
		log.Error("Could not retrieve system ID")
	}
	log.Error("System ID:", system_id)
	log.Debug("GUIDHash: ", GUIDHash(HOSTNAME))
	db_created := auto_updater.CreateDbIfNotExists(DB_FILE)
	if db_created {
		log.Info("DB Created at path: ", DB_FILE)
	}
	//	db, db_open_err := sql.Open("sqlite3", DB_FILE)
	db1, db_open_err := sql.Open("sqlite3", fmt.Sprintf("file:%s?cache=shared&mode=rwc", DB_FILE))
	//db2, db_open_err2 := sql.Open("sqlite3", fmt.Sprintf("file:%s?cache=shared&mode=rwc", DB_FILE))
	if db_open_err != nil {
		panic("Unable to open existing database")
	}
	/*if db_open_err2 != nil {
		panic("Unable to open existing database")
	}*/
	state, _ := auto_updater.GetMostRecentState(db1)
	log.Debug(state)
	var current_locked = false
	start_state, start_state_err := auto_updater.GetMostRecentState(db1)
	if start_state.Finished == 0 && start_state.Id > 0 {
		log.Error("asdf", start_state.Id)
		current_locked = true
	}
	if start_state_err != nil && start_state.Finished == 0 {
		if start_state_err.Error() != "sql: no rows in result set" {
			current_locked = true
		}
	}
	log.Debug("At INIT: current_locked:", current_locked)
	if !util.HasScriptPath(SCRIPTPATH) {
		log.Error(fmt.Sprintf("Script Path %s does not exist.", SCRIPTPATH))
		os.Exit(2)
	}
	go auto_updater.DBPoll(db1, HOSTNAME, APIURL, 0)
	for {
		log.Debug("In LOOP: current_locked:", current_locked)
		cron_line, cron_err := util.ReadCronFile(CRONFILE)
		if cron_err != nil {
			log.Debug(fmt.Sprintf("%s", cron_err))
			os.Exit(2)
		}

		run_now, run_after, sleep_seconds := cron_eval.EvalCronLine(cron_line)
		if run_now == false && run_after == false && current_locked == false {
			time.Sleep(time.Duration(sleep_seconds) * time.Second)
			continue
		}

		if run_now == false && run_after == true && current_locked == false {
			time.Sleep(time.Duration(sleep_seconds) * time.Second)
		}

		scripts, _ := filepath.Glob(fmt.Sprintf("%s/*", SCRIPTPATH))
		UpdateScripts := []parse_update_script.UpdateScript{}

		var run_next = false
		for i := 0; i < len(scripts); i++ {
			script_path := scripts[i]
			if !util.ScriptValid(script_path) {
				continue
			}
			if run_next == false && current_locked && script_path != start_state.Last_script_completed && start_state.Last_script_completed != "" {
				// We are locked from running scripts
				run_next = false
				continue
			} else if current_locked && script_path == start_state.Last_script_completed && start_state.Last_script_completed != "" {
				// We are locked from running scripts
				// The last ran script is the current in the stack
				// unlock and continue to the next runnable script
				run_next = true
				continue
			} else {
				run_next = true
			}
			var uf parse_update_script.UpdateScriptFile
			var us parse_update_script.UpdateScript
			uf.FilePath = script_path
			parse_update_script.ReadFile(&uf)
			us.ParseScript(&uf)
			UpdateScripts = append(UpdateScripts, us)
		}
		exec_chan := make(chan UpdateScriptResponse)
		go ProcessEntry(exec_chan, db1)
		should_reboot := false
		for i := 0; i < len(UpdateScripts); i++ {
			if should_reboot == true {
				continue
			}
			exec_script := UpdateScripts[i].FilePath
			ret_code, stdout, stderr := auto_updater.ExecCommand(exec_script)
			usr := UpdateScriptResponse{}
			usr.ret_code = ret_code
			usr.stderr = stderr
			usr.stdout = stdout
			usr.update_script = &UpdateScripts[i]
			usr.db = db1
			usr.system_id = system_id
			usr.api_url = APIURL
			log.Error("current_locked:", current_locked)
			if i == 0 && !current_locked {
				usr.is_start = true
			} else {
				usr.is_start = false
			}
			if i == len(UpdateScripts)-1 {
				usr.is_end = true
			} else {
				usr.is_end = false
			}
			exec_chan <- usr
			go ProcessEntry(exec_chan, db1)
			//auto_updater.ProcessLog(update_guid, ret_code, stdout, stderr, &UpdateScripts[i], db2)
			exit_code_to_i, exit_code_to_i_err := strconv.Atoi(UpdateScripts[i].ScriptExitCodeReboot)
			//state.SetLastScriptCompleted(db1, exec_script)

			if exit_code_to_i_err != nil {
				panic(exit_code_to_i_err)
			}
			if exit_code_to_i == ret_code {
				SystemReboot()
				should_reboot = true
			}

		}
		current_locked = false
		//state.Finish(db1)
		//time.Sleep(10 * time.Minute)
	}
}