package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Структура для парсинга XML
type Root struct {
	Rows []Row `xml:"row"`
}

type Row struct {
	ID        int    `xml:"id"`
	FirstName string `xml:"first_name"`
	LastName  string `xml:"last_name"`
	Age       int    `xml:"age"`
	About     string `xml:"about"`
	Gender    string `xml:"gender"`
}

// Конвертирует Row в User
func (r Row) toUser() User {
	return User{
		Id:     r.ID,
		Name:   r.FirstName + " " + r.LastName,
		Age:    r.Age,
		About:  r.About,
		Gender: r.Gender,
	}
}

func loadUsers() ([]User, error) {
	data, err := os.ReadFile("dataset.xml")
	if err != nil {
		return nil, err
	}
	root := Root{}
	err = xml.Unmarshal(data, &root)
	if err != nil {
		return nil, err
	}
	users := make([]User, 0, len(root.Rows))
	for _, row := range root.Rows {
		users = append(users, row.toUser())
	}
	return users, nil
}

func filterUsers(users []User, query string) []User {
	if query == "" {
		return users
	}
	result := make([]User, 0)
	for _, user := range users {
		if strings.Contains(user.Name, query) || strings.Contains(user.About, query) {
			result = append(result, user)
		}
	}
	return result
}

func sortUsers(users []User, orderField string, orderBy int) error {
	if orderBy == OrderByAsIs {
		return nil
	}

	var less func(i, j int) bool
	switch orderField {
	case "", "Name":
		less = func(i, j int) bool {
			if orderBy == OrderByAsc {
				return users[i].Name < users[j].Name
			}
			return users[i].Name > users[j].Name
		}
	case "Id":
		less = func(i, j int) bool {
			if orderBy == OrderByAsc {
				return users[i].Id < users[j].Id
			}
			return users[i].Id > users[j].Id
		}
	case "Age":
		less = func(i, j int) bool {
			if orderBy == OrderByAsc {
				return users[i].Age < users[j].Age
			}
			return users[i].Age > users[j].Age
		}
	default:
		return fmt.Errorf("ErrorBadOrderField")
	}

	sort.Slice(users, less)
	return nil
}

// Применяет offset и limit
func paginateUsers(users []User, offset, limit int) []User {
	if offset >= len(users) {
		return []User{}
	}
	end := offset + limit
	if end > len(users) {
		end = len(users)
	}
	return users[offset:end]
}

// SearchServer - обработчик HTTP-запросов
func encodeError(w http.ResponseWriter, statusCode int, errMsg string) {
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(SearchErrorResponse{Error: errMsg}); err != nil {
		http.Error(w, fmt.Sprintf("Failed to encode error response: %v", err), http.StatusInternalServerError)
	}
}

// SearchServer - обработчик HTTP-запросов
func SearchServer(w http.ResponseWriter, r *http.Request) {
	// Проверка авторизации
	if r.Header.Get("AccessToken") != "test_token" {
		encodeError(w, http.StatusUnauthorized, "Bad AccessToken")
		return
	}

	query := r.URL.Query().Get("query")
	orderField := r.URL.Query().Get("order_field")
	orderByStr := r.URL.Query().Get("order_by")
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	if limitStr == "" || offsetStr == "" || orderByStr == "" {
		encodeError(w, http.StatusBadRequest, "missing required parameters")
		return
	}

	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		encodeError(w, http.StatusBadRequest, "limit must be integer")
		return
	}

	offset, err := strconv.Atoi(offsetStr)
	if err != nil {
		encodeError(w, http.StatusBadRequest, "offset must be integer")
		return
	}

	orderBy, err := strconv.Atoi(orderByStr)
	if err != nil {
		encodeError(w, http.StatusBadRequest, "order_by must be integer")
		return
	}

	// Валидация значений orderBy
	if orderBy != OrderByAsc && orderBy != OrderByAsIs && orderBy != OrderByDesc {
		encodeError(w, http.StatusBadRequest, "order_by invalid")
		return
	}

	// Загрузка данных
	users, err := loadUsers()
	if err != nil {
		encodeError(w, http.StatusInternalServerError, "failed to load data")
		return
	}

	// Фильтрация
	filtered := filterUsers(users, query)

	// Сортировка
	err = sortUsers(filtered, orderField, orderBy)
	if err != nil {
		encodeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Пагинация (limit уже увеличен на 1 в клиенте для определения NextPage)
	result := paginateUsers(filtered, offset, limit)

	// Отправка ответа
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(result); err != nil {
		// При ошибке кодирования успешного ответа, статус уже отправлен
		// Поэтому просто логируем
		fmt.Printf("Failed to encode successful response: %v\n", err)
	}
}

// TestSearchServer - тесты для SearchServer
func TestSearchServer(t *testing.T) {
	// Создаем тестовый сервер как в примере server_test.go
	ts := httptest.NewServer(http.HandlerFunc(SearchServer))
	defer ts.Close()

	// Создаем клиента
	client := SearchClient{
		AccessToken: "test_token",
		URL:         ts.URL,
	}

	// Табличные тесты
	testCases := []struct {
		name       string
		request    SearchRequest
		expectLen  int                                      // ожидаемое количество пользователей
		expectNext bool                                     // ожидаемое значение NextPage
		expectErr  string                                   // ожидаемая ошибка (пусто - нет ошибки)
		validate   func(t *testing.T, resp *SearchResponse) // дополнительная валидация
	}{
		{
			name: "basic search",
			request: SearchRequest{
				Limit:      10,
				Offset:     0,
				Query:      "",
				OrderField: "",
				OrderBy:    OrderByAsc,
			},
			expectLen:  10,
			expectNext: true,
			validate: func(t *testing.T, resp *SearchResponse) {
				// Проверяем сортировку по умолчанию (Name asc)
				for i := 1; i < len(resp.Users); i++ {
					if resp.Users[i-1].Name > resp.Users[i].Name {
						t.Errorf("Users not sorted by Name asc")
					}
				}
			},
		},
		{
			name: "search with query",
			request: SearchRequest{
				Limit:      5,
				Offset:     0,
				Query:      "Boyd",
				OrderField: "Id",
				OrderBy:    OrderByAsc,
			},
			expectLen:  1, // Boyd Wolf только один
			expectNext: false,
			validate: func(t *testing.T, resp *SearchResponse) {
				if len(resp.Users) > 0 && resp.Users[0].Name != "Boyd Wolf" {
					t.Errorf("Expected Boyd Wolf, got %s", resp.Users[0].Name)
				}
			},
		},
		{
			name: "pagination next page exists",
			request: SearchRequest{
				Limit:      5,
				Offset:     0,
				Query:      "",
				OrderField: "Id",
				OrderBy:    OrderByAsc,
			},
			expectLen:  5,
			expectNext: true,
		},
		{
			name: "pagination last page",
			request: SearchRequest{
				Limit:      10,
				Offset:     30, // всего 35 записей
				Query:      "",
				OrderField: "Id",
				OrderBy:    OrderByAsc,
			},
			expectLen:  5,
			expectNext: false,
		},
		{
			name: "offset beyond data",
			request: SearchRequest{
				Limit:      10,
				Offset:     100,
				Query:      "",
				OrderField: "Id",
				OrderBy:    OrderByAsc,
			},
			expectLen:  0,
			expectNext: false,
		},
		{
			name: "sort by Age desc",
			request: SearchRequest{
				Limit:      35,
				Offset:     0,
				Query:      "",
				OrderField: "Age",
				OrderBy:    OrderByDesc,
			},
			expectLen:  25,   // клиент ограничивает до 25
			expectNext: true, // так как есть еще записи (всего 35, вернули 25)
			validate: func(t *testing.T, resp *SearchResponse) {
				// Проверяем сортировку
				for i := 1; i < len(resp.Users); i++ {
					if resp.Users[i-1].Age < resp.Users[i].Age {
						t.Errorf("Users not sorted by Age desc: %d < %d",
							resp.Users[i-1].Age, resp.Users[i].Age)
					}
				}
			},
		},
		{
			name: "invalid order field",
			request: SearchRequest{
				Limit:      10,
				Offset:     0,
				Query:      "",
				OrderField: "Invalid",
				OrderBy:    OrderByAsc,
			},
			expectErr: "OrderFeld Invalid invalid",
		},
		{
			name: "limit negative",
			request: SearchRequest{
				Limit:      -1,
				Offset:     0,
				Query:      "",
				OrderField: "",
				OrderBy:    OrderByAsc,
			},
			expectErr: "limit must be > 0",
		},
		{
			name: "offset negative",
			request: SearchRequest{
				Limit:      10,
				Offset:     -1,
				Query:      "",
				OrderField: "",
				OrderBy:    OrderByAsc,
			},
			expectErr: "offset must be > 0",
		},
		{
			name: "limit exceeds max",
			request: SearchRequest{
				Limit:      50, // больше 25
				Offset:     0,
				Query:      "",
				OrderField: "",
				OrderBy:    OrderByAsc,
			},
			expectLen:  25, // должно быть ограничено до 25
			expectNext: true,
		},
	}

	// Запуск тестов
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := client.FindUsers(tc.request)

			// Проверка ошибки
			if tc.expectErr != "" {
				if err == nil {
					t.Errorf("Expected error, got nil")
				} else if err.Error() != tc.expectErr {
					t.Errorf("Expected error '%s', got '%s'", tc.expectErr, err.Error())
				}
				return
			}

			// Проверка отсутствия ошибки
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			// Проверка количества пользователей
			if len(resp.Users) != tc.expectLen {
				t.Errorf("Expected %d users, got %d", tc.expectLen, len(resp.Users))
			}

			// Проверка NextPage
			if resp.NextPage != tc.expectNext {
				t.Errorf("Expected NextPage=%v, got %v", tc.expectNext, resp.NextPage)
			}

			// Дополнительная валидация
			if tc.validate != nil {
				tc.validate(t, resp)
			}
		})
	}
}

// TestErrorCases - тесты для обработки ошибок клиента
func TestErrorCases(t *testing.T) {
	// Тест с неверным токеном
	t.Run("invalid token", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(SearchServer))
		defer ts.Close()

		client := SearchClient{
			AccessToken: "invalid_token",
			URL:         ts.URL,
		}

		_, err := client.FindUsers(SearchRequest{
			Limit:      10,
			Offset:     0,
			Query:      "",
			OrderField: "",
			OrderBy:    OrderByAsc,
		})

		if err == nil || err.Error() != "Bad AccessToken" {
			t.Errorf("Expected 'Bad AccessToken' error, got %v", err)
		}
	})

	// Тест с неверным URL
	t.Run("invalid URL", func(t *testing.T) {
		client := SearchClient{
			AccessToken: "test_token",
			URL:         "http://localhost:9999", // неверный порт
		}

		_, err := client.FindUsers(SearchRequest{
			Limit:      10,
			Offset:     0,
			Query:      "",
			OrderField: "",
			OrderBy:    OrderByAsc,
		})

		if err == nil {
			t.Errorf("Expected error, got nil")
		}
	})

	// Тест с некорректным JSON ответом
	t.Run("invalid JSON", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("invalid json"))
		}))
		defer ts.Close()

		client := SearchClient{
			AccessToken: "test_token",
			URL:         ts.URL,
		}

		_, err := client.FindUsers(SearchRequest{
			Limit:      10,
			Offset:     0,
			Query:      "",
			OrderField: "",
			OrderBy:    OrderByAsc,
		})

		if err == nil {
			t.Errorf("Expected error, got nil")
		}
	})

	// Тест с ошибкой 500
	t.Run("internal server error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer ts.Close()

		client := SearchClient{
			AccessToken: "test_token",
			URL:         ts.URL,
		}

		_, err := client.FindUsers(SearchRequest{
			Limit:      10,
			Offset:     0,
			Query:      "",
			OrderField: "",
			OrderBy:    OrderByAsc,
		})

		if err == nil || err.Error() != "SearchServer fatal error" {
			t.Errorf("Expected 'SearchServer fatal error', got %v", err)
		}
	})

	// Тест с некорректной ошибкой 400
	t.Run("bad request invalid json", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("invalid json"))
		}))
		defer ts.Close()

		client := SearchClient{
			AccessToken: "test_token",
			URL:         ts.URL,
		}

		_, err := client.FindUsers(SearchRequest{
			Limit:      10,
			Offset:     0,
			Query:      "",
			OrderField: "",
			OrderBy:    OrderByAsc,
		})

		if err == nil {
			t.Errorf("Expected error, got nil")
		}
	})

	// Тест с неизвестной ошибкой 400
	t.Run("bad request unknown error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(SearchErrorResponse{Error: "unknown error"})
		}))
		defer ts.Close()

		client := SearchClient{
			AccessToken: "test_token",
			URL:         ts.URL,
		}

		_, err := client.FindUsers(SearchRequest{
			Limit:      10,
			Offset:     0,
			Query:      "",
			OrderField: "",
			OrderBy:    OrderByAsc,
		})

		if err == nil || err.Error() != "unknown bad request error: unknown error" {
			t.Errorf("Expected 'unknown bad request error: unknown error', got %v", err)
		}
	})
}

// TestCoverage - проверка покрытия
func TestCoverage(t *testing.T) {
	t.Log("Run: go test -coverprofile=cover.out && go tool cover -html=cover.out -o cover.html")
}
func TestEncodeErrors(t *testing.T) {
	// Создаем кастомный ResponseWriter, который всегда возвращает ошибку при записи
	errorWriter := &errorResponseWriter{
		header: http.Header{},
	}

	// Вызываем SearchServer с нашим кастомным writer
	req := httptest.NewRequest("GET", "/?limit=10&offset=0&order_by=1", nil)
	req.Header.Set("AccessToken", "test_token")

	// Должно отработать без паники
	SearchServer(errorWriter, req)
}

// errorResponseWriter - кастомный ResponseWriter для тестирования ошибок записи
type errorResponseWriter struct {
	header http.Header
}

func (e *errorResponseWriter) Header() http.Header {
	return e.header
}

func (e *errorResponseWriter) Write([]byte) (int, error) {
	return 0, fmt.Errorf("simulated write error")
}

func (e *errorResponseWriter) WriteHeader(statusCode int) {
	// ничего не делаем
}
