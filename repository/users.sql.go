// Code generated by sqlc. DO NOT EDIT.
// versions:
//   sqlc v1.29.0
// source: users.sql

package repository

import (
	"context"
)

const createUser = `-- name: CreateUser :one
INSERT INTO users (id, username, first_name, last_name, email, phone)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, email, username, first_name, last_name, added_at, updated_at, phone
`

type CreateUserParams struct {
	ID        string  `json:"id"`
	Username  *string `json:"username"`
	FirstName *string `json:"first_name"`
	LastName  *string `json:"last_name"`
	Email     string  `json:"email"`
	Phone     *string `json:"phone"`
}

func (q *Queries) CreateUser(ctx context.Context, arg CreateUserParams) (User, error) {
	row := q.db.QueryRow(ctx, createUser,
		arg.ID,
		arg.Username,
		arg.FirstName,
		arg.LastName,
		arg.Email,
		arg.Phone,
	)
	var i User
	err := row.Scan(
		&i.ID,
		&i.Email,
		&i.Username,
		&i.FirstName,
		&i.LastName,
		&i.AddedAt,
		&i.UpdatedAt,
		&i.Phone,
	)
	return i, err
}

const getAllUsers = `-- name: GetAllUsers :many
SELECT id, email, username, first_name, last_name, added_at, updated_at, phone FROM users
`

func (q *Queries) GetAllUsers(ctx context.Context) ([]User, error) {
	rows, err := q.db.Query(ctx, getAllUsers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []User
	for rows.Next() {
		var i User
		if err := rows.Scan(
			&i.ID,
			&i.Email,
			&i.Username,
			&i.FirstName,
			&i.LastName,
			&i.AddedAt,
			&i.UpdatedAt,
			&i.Phone,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const getUserById = `-- name: GetUserById :one
SELECT id, email, username, first_name, last_name, added_at, updated_at, phone FROM users
WHERE id = $1
`

func (q *Queries) GetUserById(ctx context.Context, id string) (User, error) {
	row := q.db.QueryRow(ctx, getUserById, id)
	var i User
	err := row.Scan(
		&i.ID,
		&i.Email,
		&i.Username,
		&i.FirstName,
		&i.LastName,
		&i.AddedAt,
		&i.UpdatedAt,
		&i.Phone,
	)
	return i, err
}
