module github.com/foxcpp/go-imap-mess

go 1.14

require (
	github.com/emersion/go-imap v1.2.2-0.20220928192137-6fac715be9cf
	github.com/emersion/go-imap-appendlimit v0.0.0-20190308131241-25671c986a6a // indirect
	github.com/emersion/go-imap-move v0.0.0-20180601155324-5eb20cb834bf // indirect
	github.com/emersion/go-message v0.15.0
	github.com/foxcpp/go-imap-backend-tests v0.0.0-20220105184719-e80aa29a5e16
)

replace github.com/emersion/go-imap => github.com/foxcpp/go-imap v1.0.0-beta.1.0.20220623182312-df940c324887
