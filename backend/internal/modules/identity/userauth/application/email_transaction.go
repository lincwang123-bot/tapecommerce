package application

import (
	"context"
	"errors"
)

// The preliminary check persists invalid-attempt accounting. A second check and
// conditional consumption run with the account mutation, so a database failure
// rolls back both the code and the account, without rolling back bad attempts.
func (s *Service) withVerifiedEmailCode(email, purpose, code string, mutate func(AuthTransaction) error) error {
	if _, err := s.checkCode(email, purpose, code); err != nil {
		return err
	}
	if s.authUnitOfWork == nil {
		return errors.New("email verification transaction unavailable")
	}
	return s.authUnitOfWork.WithinTransaction(context.Background(), func(tx AuthTransaction) error {
		provider, ok := tx.(VerificationCodeTransaction)
		if !ok {
			return errors.New("email verification transaction unavailable")
		}
		transactional := *s
		transactional.codeRepo = provider.VerificationCodes()
		if _, err := transactional.verifyCode(email, purpose, code); err != nil {
			return err
		}
		return mutate(tx)
	})
}
