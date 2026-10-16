package contract

import "time"

import apicredentialdomain "github.com/dujiao-next/internal/modules/apicredential/domain"

type ListFilter struct {
	Status   string
	UserID   uint
	Search   string
	Page     int
	PageSize int
}

type Repository interface {
	TouchLastUsed(id uint, at time.Time) error
	GetByID(id uint) (*apicredentialdomain.ApiCredential, error)
	GetByUserID(userID uint) (*apicredentialdomain.ApiCredential, error)
	GetAnyByUserID(userID uint) (*apicredentialdomain.ApiCredential, error)
	GetByApiKey(apiKey string) (*apicredentialdomain.ApiCredential, error)
	Create(credential *apicredentialdomain.ApiCredential) error
	Update(credential *apicredentialdomain.ApiCredential) error
	UpdateAny(credential *apicredentialdomain.ApiCredential) error
	Delete(id uint) error
	List(filter ListFilter) ([]apicredentialdomain.ApiCredential, int64, error)
}
