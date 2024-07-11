package main

import "text/template"

var templates = template.Must(template.ParseFiles("tmpl/sth.html", "tmpl/sth2.html"))
