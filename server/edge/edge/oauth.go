package edge

import (
     "encoding/json"
     "fmt"
     "bytes"
     "io"
     "errors"
     "strconv"
     "net/http"
     "net/http/httputil"
     "net/url"
     "os"
     "time"
     "context"
     "github.com/google/uuid"
     "github.com/go-oauth2/oauth2/generates"
     "github.com/go-oauth2/oauth2/manage"
     "github.com/go-oauth2/oauth2/models"
     "github.com/go-oauth2/oauth2/server"
     "github.com/go-oauth2/oauth2/store"
     "github.com/go-redis/redis"
     "github.com/go-session/session"
)

var (
     Dumpvar      bool
     Domainvar    string
     clientStore  *store.ClientStore
     credDB       *redis.Client
     srv          *server.Server
     manager      *manage.Manager
     ctx          context.Context
     ttl          time.Duration
)

func init() {
     credDB = redis.NewClient(&redis.Options {
          Addr: "redis:6379",
          DB: 1,
     })
     ctx = context.Background()
     clientStore = store.NewClientStore()
     manager = manage.NewDefaultManager()
     manager.SetAuthorizeCodeTokenCfg(manage.DefaultAuthorizeCodeTokenCfg)
     manager.MustTokenStorage(store.NewMemoryTokenStore())
     manager.MapAccessGenerate(generates.NewAccessGenerate())
     manager.MapClientStorage(clientStore)
     srv = server.NewServer(server.NewConfig(), manager)
     srv.SetPasswordAuthorizationHandler(validateCredentialsHandler)
     srv.SetUserAuthorizationHandler(userAuthorizeHandler)
     ttl = time.Hour
}

type FeedCredentials struct {
     Password string     `json:"password"`
     UserId string       `json:"userid"`
}

func AuthorizeHandler(w http.ResponseWriter, r *http.Request) {
     if Dumpvar {
          dumpRequest(os.Stdout, "authorize", r)
     }
     store, err := session.Start(r.Context(), w, r)
     if err != nil {
          http.Error(w, err.Error(), http.StatusInternalServerError)
          return
     }
     var form url.Values
     if v, ok := store.Get("ReturnUri"); ok {
          form = v.(url.Values)
     }
     r.Form = form
     store.Delete("ReturnUri")
     store.Save()
     err = srv.HandleAuthorizeRequest(w, r)
     if err != nil {
          http.Error(w, err.Error(), http.StatusBadRequest)
     }
}

func TokenHandler(w http.ResponseWriter, r *http.Request) {
     if Dumpvar {
          _ = dumpRequest(os.Stdout, "token", r) // Ignore the error
     }
     err := srv.HandleTokenRequest(w, r)
     if err != nil {
          http.Error(w, err.Error(), http.StatusInternalServerError)
     }
}

func TestHandler(w http.ResponseWriter, r *http.Request) {
     if Dumpvar {
          _ = dumpRequest(os.Stdout, "test", r) // Ignore the error
     }
     token, err := srv.ValidationBearerToken(r)
     if err != nil {
          http.Error(w, err.Error(), http.StatusBadRequest)
          return
     }
     data := map[string]interface{}{
          "expires_in": int64(token.GetAccessCreateAt().Add(token.GetAccessExpiresIn()).Sub(time.Now()).Seconds()),
          "client_id":  token.GetClientID(),
          "user_id":    token.GetUserID(),
     }
     e := json.NewEncoder(w)
     e.SetIndent("", "  ")
     e.Encode(data)
}
     
func validateCredentialsHandler(username, password string) (userID string, err error) {
     var fc FeedCredentials
     c, err := credDB.Get(username).Result()
     switch {
     case err == redis.Nil:
       ip := Participant{0, username, ""}
       b, err := json.Marshal(ip)
       if err != nil {
          return "", errors.New("username not well founded")
       }
       resp, err := http.Post("http://feed:8080/participant", "application/json", bytes.NewReader(b))
       if err != nil {
          return "", errors.New("cannot create participant")
       }
       var p Participant
       body, err := io.ReadAll(resp.Body)
       if err != nil {
          return "", errors.New("cannot fetch response from create participant call")
       }
       err = json.Unmarshal([]byte(string(body)), &p)
       if err != nil {
          return "", errors.New("create participant invalid response")
       }
       sid := strconv.FormatInt(p.Id, 10)
       fc = FeedCredentials {password, sid}
       bb, err := json.Marshal(fc)
       if err != nil {
          return "", errors.New("password has illegal characters")
       }
       credDB.Set(username, bb, ttl)
       u, err := uuid.NewRandom()
       if err != nil {
          return "", errors.New("cannot generate secret")
       }
       secret := fmt.Sprintf("%s", u)
       clientStore.Set(sid, &models.Client{
         ID:     sid,
         Secret: secret,
         Domain: Domainvar,
       })
       return sid, nil
     case err == nil:
       return "", errors.New("cannot connect to redis")
     case c == "":
       return "", errors.New("invalid credentials format")
     default:
       err = json.Unmarshal([]byte(c), &fc)
       if err != nil {
          return "", errors.New("invalid credentials schema")
       } else {
         if fc.Password == password {
            return fc.UserId, nil
         } else {
            return "", errors.New("incorrect password")
         }
       }
     }
}

func dumpRequest(writer io.Writer, header string, r *http.Request) error {
     data, err := httputil.DumpRequest(r, true)
     if err != nil {
          return err
     }
     writer.Write([]byte("\n" + header + ": \n"))
     writer.Write(data)
     return nil
}

func userAuthorizeHandler(w http.ResponseWriter, r *http.Request) (userID string, err error) {
     if Dumpvar {
          _ = dumpRequest(os.Stdout, "userAuthorizeHandler", r) // Ignore the error
     }
     store, err := session.Start(r.Context(), w, r)
     if err != nil {
          return
     }

     uid, ok := store.Get("LoggedInUserID")
     if !ok {
          if r.Form == nil {
               r.ParseForm()
          }

          store.Set("ReturnUri", r.Form)
          store.Save()

          w.Header().Set("Location", "/login")
          w.WriteHeader(http.StatusFound)
          return
     }

     userID = uid.(string)
     store.Delete("LoggedInUserID")
     store.Save()
     return
}

func LoginHandler(w http.ResponseWriter, r *http.Request) {
     if Dumpvar {
          _ = dumpRequest(os.Stdout, "login", r) // Ignore the error
     }
     store, err := session.Start(r.Context(), w, r)
     if err != nil {
          http.Error(w, err.Error(), http.StatusInternalServerError)
          return
     }

     if r.Method == "POST" {
          if r.Form == nil {
               if err := r.ParseForm(); err != nil {
                    http.Error(w, err.Error(), http.StatusInternalServerError)
                    return
               }
          }
          store.Set("LoggedInUserID", r.Form.Get("username"))
          store.Save()

          w.Header().Set("Location", "/auth")
          w.WriteHeader(http.StatusFound)
          return
     }
     outputHTML(w, r, "static/login.html")
}

func AuthHandler(w http.ResponseWriter, r *http.Request) {
     if Dumpvar {
          _ = dumpRequest(os.Stdout, "auth", r) // Ignore the error
     }
     store, err := session.Start(nil, w, r)
     if err != nil {
          http.Error(w, err.Error(), http.StatusInternalServerError)
          return
     }

     if _, ok := store.Get("LoggedInUserID"); !ok {
          w.Header().Set("Location", "/login")
          w.WriteHeader(http.StatusFound)
          return
     }

     outputHTML(w, r, "static/auth.html")
}

func outputHTML(w http.ResponseWriter, req *http.Request, filename string) {
     file, err := os.Open(filename)
     if err != nil {
          http.Error(w, err.Error(), 500)
          return
     }
     defer file.Close()
     fi, _ := file.Stat()
     http.ServeContent(w, req, file.Name(), fi.ModTime(), file)
}
