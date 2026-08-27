package main

import (
	"fmt"
	"log"
	"os"

	"github.com/balacer/backend/internal/db"
	"github.com/balacer/backend/internal/models"
	"golang.org/x/crypto/bcrypt"
)

func printUsage() {
	fmt.Println("Usage: balacer-cli <resource> <action> [args...]")
	fmt.Println("\nCommands:")
	fmt.Println("  users list                               List all users")
	fmt.Println("  users add <username> <password> [role]   Create a new user (role defaults to Admin)")
	fmt.Println("  users passwd <username> <new_password>   Change a user's password")
	os.Exit(1)
}

func main() {
	if len(os.Args) < 3 {
		printUsage()
	}

	resource := os.Args[1]
	action := os.Args[2]

	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		dsn = "postgres://balacer:balacer@localhost:5432/balacer?sslmode=disable"
	}

	database, err := db.InitDB(dsn)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	if resource == "users" {
		switch action {
		case "list":
			var users []models.User
			database.Find(&users)
			fmt.Println("ID\tUsername\tRole")
			fmt.Println("--\t--------\t----")
			for _, u := range users {
				fmt.Printf("%d\t%s\t\t%s\n", u.ID, u.Username, u.Role)
			}
		case "add":
			if len(os.Args) < 5 {
				fmt.Println("Error: missing arguments for 'users add'")
				printUsage()
			}
			username := os.Args[3]
			password := os.Args[4]
			role := "Admin"
			if len(os.Args) >= 6 {
				role = os.Args[5]
			}

			hashed, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			user := models.User{
				Username: username,
				Password: string(hashed),
				Role:     role,
			}
			if err := database.Create(&user).Error; err != nil {
				log.Fatalf("Failed to create user: %v", err)
			}
			fmt.Printf("User '%s' created successfully.\n", username)

		case "passwd":
			if len(os.Args) < 5 {
				fmt.Println("Error: missing arguments for 'users passwd'")
				printUsage()
			}
			username := os.Args[3]
			newPassword := os.Args[4]

			var user models.User
			if err := database.Where("username = ?", username).First(&user).Error; err != nil {
				log.Fatalf("User '%s' not found.", username)
			}

			hashed, _ := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
			user.Password = string(hashed)
			if err := database.Save(&user).Error; err != nil {
				log.Fatalf("Failed to update password: %v", err)
			}
			fmt.Printf("Password for '%s' updated successfully.\n", username)

		default:
			fmt.Printf("Unknown action for users: %s\n", action)
			printUsage()
		}
	} else {
		fmt.Printf("Unknown resource: %s\n", resource)
		printUsage()
	}
}
