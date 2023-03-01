package httperror

import (
	"errors"
	"fmt"
	"log"
	"net/http"
)

type httpError struct {
	err  error
	code int
}

func (e httpError) Code() int     { return e.code }
func (e httpError) Error() string { return e.err.Error() }

func newStatusError(code int, message string, cause error) HttpError {
	if message == "" {
		message = http.StatusText(code)
	}
	err := httpError{
		code: code,
		err:  cause,
	}
	if cause != nil {
		err.err = fmt.Errorf("%v: %v", message, cause)
	} else {
		err.err = errors.New(message)
	}
	return err
}

func StatusNotFound(message string, root error) HttpError {
	return newStatusError(http.StatusNotFound, message, root)
}
func StatusUnprocessableEntity(message string, root error) HttpError {
	return newStatusError(http.StatusUnprocessableEntity, message, root)
}
func StatusInternalServerError(message string, root error) HttpError {
	return newStatusError(http.StatusInternalServerError, message, root)
}

type HttpError interface {
	error
	Code() int
}

type HandleFuncWithError func(w http.ResponseWriter, r *http.Request) HttpError

func (f HandleFuncWithError) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("req: %+v", r)
	err := f(w, r)
	if err != nil {
		log.Printf("ERROR: %+v", err)
		http.Error(w, err.Error(), err.Code())
	}
}
