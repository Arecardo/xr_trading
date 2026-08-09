"""Account-binding domain subpackage. See ``domain.accounts.models`` for the frozen interface."""

from domain.accounts.models import AccountBinding, AccountBindingRepository

__all__ = ["AccountBinding", "AccountBindingRepository"]
