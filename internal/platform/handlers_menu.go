package platform

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"tablekit.de/tablekit/internal/auth"
	"tablekit.de/tablekit/internal/db/sqlcgen"
	"tablekit.de/tablekit/internal/media"
	"tablekit.de/tablekit/web/templates"
)

// mediaUploadRequestLimit bounds the multipart request size (upload limit plus
// room for form overhead).
const mediaUploadRequestLimit = media.MaxUploadBytes + (1 << 20)

func (s *Server) handleMenuPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.menuPage(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, r, http.StatusOK, templates.MenuPage(p))
}

func (s *Server) renderMenuError(w http.ResponseWriter, r *http.Request, key string) {
	p, err := s.menuPage(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p.Error = s.tr(r, key)
	s.render(w, r, http.StatusBadRequest, templates.MenuPage(p))
}

func (s *Server) menuPage(r *http.Request) (templates.Page, error) {
	id, _ := auth.FromContext(r.Context())
	p := s.page(r, "menu.title")
	p.Nav = "menu"
	p.MediaEnabled = s.mediaSvc.Configured()
	if r.URL.Query().Get("saved") == "1" {
		p.Notice = s.tr(r, "menu.created")
	}
	if r.URL.Query().Get("deleted") == "1" {
		p.Notice = s.tr(r, "menu.deleted")
	}
	if r.URL.Query().Get("image") == "1" {
		p.Notice = s.tr(r, "menu.image_saved")
	}
	if r.URL.Query().Get("soldout") == "1" {
		p.Notice = s.tr(r, "menu.sold_out_saved")
	}

	categories, err := s.menuSvc.List(r.Context(), id.Merchant.ID)
	if err != nil {
		s.log.Error("list menu", "error", err)
		return p, err
	}
	for _, c := range categories {
		view := templates.MenuCategoryView{ID: c.Category.ID.String(), Name: c.Category.Name}
		for _, item := range c.Items {
			mv := templates.MenuItemView{
				ID:           item.ID.String(),
				Name:         item.Name,
				Price:        priceInput(item.PriceMinor),
				Available:    item.Available,
				SoldOutUntil: formatTime(item.SoldOutUntil),
				SoldOut:      item.SoldOutUntil.Valid && time.Now().Before(item.SoldOutUntil.Time),
			}
			if item.ImageAssetID != nil {
				if key, err := s.mediaSvc.ObjectKey(r.Context(), id.Merchant.ID, *item.ImageAssetID); err == nil {
					mv.ImageURL = s.mediaSvc.URL(key)
					mv.ThumbURL = s.mediaSvc.ThumbURL(key)
				}
			}
			view.Items = append(view.Items, mv)
		}
		p.Menu = append(p.Menu, view)
	}
	return p, nil
}

func (s *Server) handleMenuUploadImage(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	itemID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderMenuError(w, r, "menu.error.image")
		return
	}
	if !s.mediaSvc.Configured() {
		s.renderMenuError(w, r, "menu.error.image_storage")
		return
	}
	// Cap the request body slightly above the media limit so oversized
	// multipart frames are rejected before hitting disk/memory.
	r.Body = http.MaxBytesReader(w, r.Body, mediaUploadRequestLimit)
	if err := r.ParseMultipartForm(4 << 20); err != nil { // #nosec G120 -- request body is capped by MaxBytesReader
		s.renderMenuError(w, r, "menu.error.image")
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		s.renderMenuError(w, r, "menu.error.image")
		return
	}
	defer file.Close()

	previous, _ := s.menuSvc.Item(r.Context(), id.Merchant.ID, itemID)

	asset, err := s.mediaSvc.Upload(r.Context(), id.Merchant.ID, id.Staff.ID, sqlcgen.MediaKindMenuItem, file)
	if err != nil {
		s.log.Warn("menu image upload rejected", "error", err)
		switch {
		case errors.Is(err, media.ErrTooLarge):
			s.renderMenuError(w, r, "menu.error.image_size")
		case errors.Is(err, media.ErrUnsupported), errors.Is(err, media.ErrCorrupt):
			s.renderMenuError(w, r, "menu.error.image_type")
		default:
			s.renderMenuError(w, r, "menu.error.image")
		}
		return
	}
	if err := s.menuSvc.SetImage(r.Context(), id.Merchant.ID, itemID, &asset.ID); err != nil {
		s.log.Error("attach menu image", "error", err)
		_ = s.mediaSvc.Delete(r.Context(), id.Merchant.ID, asset.ID)
		s.renderMenuError(w, r, "menu.error.image")
		return
	}
	if previous.ImageAssetID != nil && *previous.ImageAssetID != asset.ID {
		if err := s.mediaSvc.Delete(r.Context(), id.Merchant.ID, *previous.ImageAssetID); err != nil {
			s.log.Warn("remove replaced menu image", "error", err)
		}
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "menu.item_image", "menu_item", itemID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/menu?image=1", http.StatusSeeOther)
}

func (s *Server) handleMenuDeleteImage(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	itemID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderMenuError(w, r, "menu.error.image")
		return
	}
	previous, err := s.menuSvc.Item(r.Context(), id.Merchant.ID, itemID)
	if err != nil {
		s.renderMenuError(w, r, "menu.error.image")
		return
	}
	if err := s.menuSvc.SetImage(r.Context(), id.Merchant.ID, itemID, nil); err != nil {
		s.log.Error("clear menu image", "error", err)
		s.renderMenuError(w, r, "menu.error.image")
		return
	}
	if previous.ImageAssetID != nil {
		if err := s.mediaSvc.Delete(r.Context(), id.Merchant.ID, *previous.ImageAssetID); err != nil {
			s.log.Warn("remove menu image", "error", err)
		}
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "menu.item_image_removed", "menu_item", itemID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/menu?image=1", http.StatusSeeOther)
}

func (s *Server) handleMenuCreateCategory(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" || len(name) > 120 {
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	if err := s.menuSvc.CreateCategory(r.Context(), id.Merchant.ID, name); err != nil {
		s.log.Error("create category", "error", err)
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "menu.category_created", "menu_category", "", s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/menu?saved=1", http.StatusSeeOther)
}

func (s *Server) handleMenuCreateItem(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	categoryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" || len(name) > 160 {
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	price, ok := parsePrice(r.PostFormValue("price"))
	if !ok {
		s.renderMenuError(w, r, "menu.error.price")
		return
	}
	if err := s.menuSvc.CreateItem(r.Context(), id.Merchant.ID, categoryID, name, price); err != nil {
		s.log.Error("create item", "error", err)
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "menu.item_created", "menu_item", "", s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/menu?saved=1", http.StatusSeeOther)
}

func (s *Server) handleMenuUpdateItem(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	itemID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" || len(name) > 160 {
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	price, ok := parsePrice(r.PostFormValue("price"))
	if !ok {
		s.renderMenuError(w, r, "menu.error.price")
		return
	}
	if err := s.menuSvc.UpdateItem(r.Context(), id.Merchant.ID, itemID, name, price); err != nil {
		s.log.Error("update item", "error", err)
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "menu.item_updated", "menu_item", itemID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/menu?saved=1", http.StatusSeeOther)
}

func (s *Server) handleMenuToggleAvailability(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	itemID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	available := r.PostFormValue("available") == "1"
	if err := s.menuSvc.SetAvailability(r.Context(), id.Merchant.ID, itemID, available); err != nil {
		s.log.Error("set availability", "error", err)
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "menu.item_availability", "menu_item", itemID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/menu?saved=1", http.StatusSeeOther)
}

func (s *Server) handleMenuSoldOut(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	itemID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	loc, _ := time.LoadLocation("Europe/Berlin")
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	var until time.Time
	switch r.PostFormValue("preset") {
	case "today":
		until = time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 0, 0, loc)
	case "evening":
		until = time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 0, 0, loc)
	case "tomorrow":
		until = time.Date(now.Year(), now.Month(), now.Day()+1, 11, 0, 0, 0, loc)
	case "custom":
		t, err := time.ParseInLocation("2006-01-02T15:04", r.PostFormValue("until"), loc)
		if err != nil {
			s.renderMenuError(w, r, "menu.error.sold_out")
			return
		}
		until = t
	default:
		s.renderMenuError(w, r, "menu.error.sold_out")
		return
	}
	if err := s.menuSvc.SetSoldOut(r.Context(), id.Merchant.ID, itemID, until, strings.TrimSpace(r.PostFormValue("reason"))); err != nil {
		s.renderMenuError(w, r, "menu.error.sold_out")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "menu.item_sold_out", "menu_item", itemID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/menu?soldout=1", http.StatusSeeOther)
}

func (s *Server) handleMenuSoldOutClear(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	itemID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	if err := s.menuSvc.ClearSoldOut(r.Context(), id.Merchant.ID, itemID); err != nil {
		s.renderMenuError(w, r, "menu.error.sold_out")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "menu.item_available", "menu_item", itemID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/menu?saved=1", http.StatusSeeOther)
}

func (s *Server) handleMenuDeleteItem(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	itemID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	if err := s.menuSvc.DeleteItem(r.Context(), id.Merchant.ID, itemID); err != nil {
		s.log.Error("delete item", "error", err)
		s.renderMenuError(w, r, "menu.error.name")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "menu.item_deleted", "menu_item", itemID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/menu?deleted=1", http.StatusSeeOther)
}
