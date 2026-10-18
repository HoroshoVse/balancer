package main

import (
	"fmt"
	"log"
	"os"

	"github.com/balancer/backend/internal/db"
	"github.com/balancer/backend/internal/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func printUsage() {
	fmt.Println("Usage: balancer-cli <resource> <action> [args...]")
	fmt.Println("\nCommands:")
	fmt.Println("  users list                               List all users")
	fmt.Println("  users add <username> <password> [role]   Create a new user (role defaults to Admin)")
	fmt.Println("  users passwd <username> <new_password>   Change a user's password")
	fmt.Println("  users role <username> <role>             Change a user's role")
	fmt.Println("  users delete <username>                  Delete a user")
	fmt.Println("\nRoles: Admin (everything), Operator (manage load balancers), Viewer (read-only)")
	os.Exit(1)
}

func validateRole(role string) {
	if models.RoleRank(role) == 0 {
		log.Fatalf("Unknown role %q: expected Admin, Operator or Viewer", role)
	}
}

func findUser(database *gorm.DB, username string) models.User {
	var user models.User
	if err := database.Where("username = ?", username).Limit(1).Find(&user).Error; err != nil || user.ID == 0 {
		log.Fatalf("User '%s' not found.", username)
	}
	return user
}

// ensureAnotherAdmin refuses to remove the last Admin.
func ensureAnotherAdmin(database *gorm.DB, user models.User) {
	if user.Role != models.RoleAdmin {
		return
	}
	var admins int64
	database.Model(&models.User{}).Where("role = ? AND id <> ?", models.RoleAdmin, user.ID).Count(&admins)
	if admins == 0 {
		log.Fatalf("'%s' is the last Admin; create another Admin first.", user.Username)
	}
}

func main() {
	if len(os.Args) < 3 {
		printUsage()
	}

	resource := os.Args[1]
	action := os.Args[2]

	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		dsn = "postgres://balancer:balancer@localhost:5432/balancer?sslmode=disable"
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
			role := models.RoleAdmin
			if len(os.Args) >= 6 {
				role = os.Args[5]
			}
			validateRole(role)

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
			user := findUser(database, os.Args[3])

			hashed, _ := bcrypt.GenerateFromPassword([]byte(os.Args[4]), bcrypt.DefaultCost)
			// Bumping the token version signs the user out everywhere
			if err := database.Model(&user).Updates(map[string]interface{}{
				"password":      string(hashed),
				"token_version": gorm.Expr("token_version + 1"),
			}).Error; err != nil {
				log.Fatalf("Failed to update password: %v", err)
			}
			fmt.Printf("Password for '%s' updated; their open sessions are signed out.\n", user.Username)

		case "role":
			if len(os.Args) < 5 {
				fmt.Println("Error: missing arguments for 'users role'")
				printUsage()
			}
			user := findUser(database, os.Args[3])
			role := os.Args[4]
			validateRole(role)
			if role != models.RoleAdmin {
				ensureAnotherAdmin(database, user)
			}
			if err := database.Model(&user).Update("role", role).Error; err != nil {
				log.Fatalf("Failed to update role: %v", err)
			}
			fmt.Printf("Role for '%s' set to %s.\n", user.Username, role)

		case "delete":
			if len(os.Args) < 4 {
				fmt.Println("Error: missing arguments for 'users delete'")
				printUsage()
			}
			user := findUser(database, os.Args[3])
			ensureAnotherAdmin(database, user)
			if err := database.Delete(&user).Error; err != nil {
				log.Fatalf("Failed to delete user: %v", err)
			}
			fmt.Printf("User '%s' deleted.\n", user.Username)

		default:
			fmt.Printf("Unknown action for users: %s\n", action)
			printUsage()
		}
	} else {
		fmt.Printf("Unknown resource: %s\n", resource)
		printUsage()
	}
}
