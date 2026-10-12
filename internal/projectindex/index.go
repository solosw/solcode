// Package projectindex supplies a bounded, local repository path map.
// It does not make model requests and never reads source contents.
package projectindex

import (
 "context"
 "os"
 "os/exec"
 "path/filepath"
 "sort"
 "strings"
 "time"
 "unicode"
)

const maxPaths = 10000

// RelevantPaths returns likely relevant project-relative paths. Git determines
// tracked/ignored paths where possible; the fallback never descends into common
// generated/vendor directories.
func RelevantPaths(ctx context.Context, root, query string, limit int) []string {
 if strings.TrimSpace(root)=="" || limit<=0 {return nil}
 if limit>40 {limit=40}
 entries:=gitPaths(ctx,root)
 if len(entries)==0 {entries=walkPaths(root)}
 if len(entries)==0 {return nil}
 terms:=queryTerms(query)
 type scored struct{ path string; score int }
 ranked:=make([]scored,0,len(entries))
 for _,p:=range entries {
  lower:=strings.ToLower(p)
  base:=strings.ToLower(filepath.Base(p))
  score:=0
  for _,term:=range terms {
   if term=="" {continue}
   if strings.Contains(base,term) {score+=8} else if strings.Contains(lower,term) {score+=3}
  }
  if strings.HasPrefix(lower,"internal/") || strings.HasPrefix(lower,"src/") {score++}
  if strings.HasSuffix(lower,"_test.go") || strings.Contains(lower,"/test/") {score--}
  ranked=append(ranked,scored{p,score})
 }
 sort.Slice(ranked,func(i,j int)bool{
  if ranked[i].score!=ranked[j].score {return ranked[i].score>ranked[j].score}
  return ranked[i].path<ranked[j].path
 })
 result:=make([]string,0,limit)
 for _,p:=range ranked {
  if len(result)>=limit {break}
  result=append(result,p.path)
 }
 return result
}

func queryTerms(query string) []string {
 tokens:=strings.FieldsFunc(strings.ToLower(query),func(r rune)bool{
  return !unicode.IsLetter(r)&&!unicode.IsDigit(r)&&r!='_'&&r!='.'&&r!='/'
 })
 out:=make([]string,0,len(tokens))
 for _,t:=range tokens {
  if len(t)>=3 && len(t)<80 {out=append(out,t)}
 }
 return out
}

func gitPaths(ctx context.Context,root string) []string {
 sub,cancel:=context.WithTimeout(ctx,1500*time.Millisecond)
 defer cancel()
 cmd:=exec.CommandContext(sub,"git","ls-files","--cached","--others","--exclude-standard")
 cmd.Dir=root
 out,err:=cmd.Output()
 if err!=nil || len(out)>2*1024*1024 {return nil}
 paths:=make([]string,0,1024)
 seen:=map[string]bool{}
 for _,p:=range strings.Split(string(out),"\n") {
  p=strings.TrimSpace(p)
  if p==""||seen[p]||strings.ContainsAny(p,"\r\x00")||strings.HasPrefix(p,"../"){continue}
  seen[p]=true
  paths=append(paths,filepath.ToSlash(p))
  if len(paths)>=maxPaths {break}
 }
 return paths
}

func walkPaths(root string) []string {
 paths:=make([]string,0,1024)
 _=filepath.WalkDir(root,func(path string, d os.DirEntry,err error)error{
  if err!=nil {return nil}
  if len(paths)>=maxPaths {return filepath.SkipAll}
  name:=d.Name()
  if d.IsDir() {
   switch name {case ".git","node_modules","vendor","dist","build",".next",".venv","coverage":
    return filepath.SkipDir
   }
   return nil
  }
  if !d.Type().IsRegular() {return nil}
  rel,e:=filepath.Rel(root,path)
  if e==nil {paths=append(paths,filepath.ToSlash(rel))}
  return nil
 })
 return paths
}

// Context returns a small, untrusted-data-labelled candidate-file index.
func Context(ctx context.Context,root,query string) string {
 paths:=RelevantPaths(ctx,root,query,20)
 if len(paths)==0 {return ""}
 return "Repository candidate paths (local path index; verify contents using BatchTool):\n- "+strings.Join(paths,"\n- ")
}
