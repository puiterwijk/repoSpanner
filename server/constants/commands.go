package constants

type Command string

const (
	CmdFetch Command = "FETCH"
	CmdRun   Command = "RUN"
	CmdQuit  Command = "QUIT"

	CmdRespOK   Command = "OK"
	CmdRespFail Command = "FAIL"
)
