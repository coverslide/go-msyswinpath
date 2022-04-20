project=msyswinpath
postfix=.exe

executable=$(project)$(postfix)

$(executable):
	go build -o $(executable) main.go
